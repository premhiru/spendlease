package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

func TestEnforceModeReturnsStructured402BeforeEgress(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	called := false
	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		called = true
		mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	}, store.ModeEnforce, money.MustParseUSD("0.001"))

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Spendlease-Error"); got != ErrTypeBudgetExceeded {
		t.Errorf("X-Spendlease-Error = %q, want %q", got, ErrTypeBudgetExceeded)
	}
	mu.Lock()
	wasCalled := called
	mu.Unlock()
	if wasCalled {
		t.Fatal("the vendor was contacted for a rejected reservation")
	}

	var body APIError
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode 402: %v", err)
	}
	d := body.Error
	if d.Run == "" || d.Principal != h.principal.ID || d.Budget != "0.001" {
		t.Errorf("402 attribution = %+v", d)
	}
	if d.Requested == "" || d.Shortfall == "" || d.Resolution == "" {
		t.Errorf("402 does not explain the amount and remedy: %+v", d)
	}
	events, err := h.store.RecentOperationalEvents(context.Background(), 10, time.Now())
	if err != nil {
		t.Fatalf("RecentOperationalEvents: %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventBudgetBlocked {
		t.Fatalf("events = %+v, want one durable budget-blocked event", events)
	}
	if events[0].PrincipalID != h.principal.ID || events[0].Amount.IsZero() {
		t.Errorf("budget event attribution = %+v", events[0])
	}
}

func TestMissingMaxTokensStillCreatesBoundedReservation(t *testing.T) {
	t.Parallel()

	arrived := make(chan struct{})
	release := make(chan struct{})
	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, _ *http.Request) {
		close(arrived)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}, store.ModeEnforce, money.MustParseUSD("1.00"))

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	done := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errs <- err
			return
		}
		done <- resp
	}()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached upstream")
	}

	runs, err := h.store.ListRunsByPrincipal(context.Background(), h.principal.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = (%v, %v), want one implicit run", runs, err)
	}
	held, err := h.store.PendingReservationTotal(context.Background(), runs[0].ID)
	if err != nil {
		t.Fatalf("pending total: %v", err)
	}
	if held <= 0 || held >= money.MustParseUSD("1.00") {
		t.Errorf("bounded default reservation = %s, want positive and below budget", held)
	}
	close(release)
	select {
	case err := <-errs:
		t.Fatalf("do request: %v", err)
	case resp := <-done:
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	}
}

func TestUnknownModelUsesFallbackInsteadOfZero(t *testing.T) {
	t.Parallel()

	called := make(chan struct{}, 1)
	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, _ *http.Request) {
		called <- struct{}{}
		_, _ = io.WriteString(w, `{}`)
	}, store.ModeEnforce, money.Nano)

	resp := h.call(t, "/v1/chat/completions",
		`{"model":"future-model-that-is-not-priced","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`, nil)
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 from fallback pricing", resp.StatusCode)
	}
	select {
	case <-called:
		t.Fatal("unknown model silently cost zero and reached the vendor")
	default:
	}
}

func TestObserveModeForwardsRequestThatWouldBlock(t *testing.T) {
	t.Parallel()

	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w,
			`{"usage":{"prompt_tokens":10,"completion_tokens":10}}`)
	}, store.ModeObserve, money.Nano)

	resp := h.call(t, "/v1/chat/completions",
		`{"model":"gpt-4o","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want observe mode to pass", resp.StatusCode)
	}
	if !strings.Contains(h.logs.String(), "would have exceeded budget") {
		t.Errorf("logs do not expose the would-block decision: %s", h.logs.String())
	}
	entries := h.entries(t, 1)
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
}

func TestProvider500ReleasesReservationAndDoesNotCharge(t *testing.T) {
	t.Parallel()

	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"vendor failed"}`)
	}, store.ModeEnforce, money.MustParseUSD("1.00"))

	resp := h.call(t, "/v1/chat/completions",
		`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`, nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want vendor 500", resp.StatusCode)
	}

	var pending int
	if err := h.store.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM reservations WHERE status = 'pending'`).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending reservations = %d after provider 500, want 0", pending)
	}
	entries, _ := h.store.LedgerEntries(context.Background(), store.LedgerFilter{})
	if len(entries) != 0 {
		t.Errorf("provider 500 produced %d ledger entries", len(entries))
	}
}

func TestMidStreamDisconnectSettlesPartialUsageAndReleasesHold(t *testing.T) {
	t.Parallel()

	upstreamDone := make(chan struct{})
	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":500,"output_tokens":1}}}

`)
		f.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
				_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"x\"}}\n\n")
				f.Flush()
			}
		}
	}, store.ModeEnforce, money.MustParseUSD("1.00"))

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.gateway.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-5","stream":true,"max_tokens":100}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	buf := make([]byte, 256)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	cancel()
	_ = resp.Body.Close()

	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not observe the disconnect")
	}
	entry := h.entries(t, 1)[0]
	if entry.InputTokens != 500 || !entry.Estimated {
		t.Errorf("partial entry = %+v, want reported input and estimated marker", entry)
	}
	var pending int
	if err := h.store.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM reservations WHERE status = 'pending'`).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("disconnect left %d pending holds", pending)
	}
}

type fakeExpirer struct {
	called chan time.Time
}

func (f *fakeExpirer) ExpirePendingReservations(_ context.Context, now time.Time) (int, error) {
	f.called <- now
	return 1, nil
}

func TestReservationSweeperRunsImmediatelyAndStops(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeExpirer{called: make(chan time.Time, 2)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	StartReservationSweeper(ctx, f, time.Hour, logger)

	select {
	case <-f.called:
	case <-time.After(time.Second):
		t.Fatal("sweeper did not run at startup")
	}
	cancel()
}
