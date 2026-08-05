package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	spendlease "github.com/premhiru/spendlease"
	"github.com/premhiru/spendlease/internal/billing"
	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/pricing"
	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
)

// recordingHarness is a gateway backed by a real store and price book, so
// ledger entries are genuinely written and can be read back.
type recordingHarness struct {
	gateway     *httptest.Server
	upstream    *httptest.Server
	store       *sqlite.Store
	logs        *syncBuffer
	principal   store.Principal
	revocations *RevocationSet
}

func newRecordingHarness(t *testing.T, upstream http.HandlerFunc) *recordingHarness {
	return newRecordingHarnessWith(t, upstream, store.ModeObserve, money.MustParseUSD("10.00"))
}

func newRecordingHarnessWith(
	t *testing.T,
	upstream http.HandlerFunc,
	mode store.Mode,
	budget money.Nanos,
) *recordingHarness {
	t.Helper()

	ctx := context.Background()

	st, err := sqlite.Open(ctx, sqlite.InMemory, sqlite.Options{})
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	principal := store.Principal{
		ID: store.NewPrincipalID(), Name: "recorder-test",
		KeyHash: store.HashSecret(testKey), Mode: mode, CreatedAt: time.Now(),
	}
	if err := st.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("creating the principal: %v", err)
	}

	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)

	oa, err := openai.NewWithBaseURL(up.URL)
	if err != nil {
		t.Fatalf("openai adapter: %v", err)
	}
	an, err := anthropic.NewWithBaseURL(up.URL)
	if err != nil {
		t.Fatalf("anthropic adapter: %v", err)
	}
	registry, err := providers.NewRegistry(oa, an)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	book, err := pricing.Load(spendlease.PriceBookFS(), spendlease.PriceBookDir, pricing.Options{})
	if err != nil {
		t.Fatalf("price book: %v", err)
	}

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	revocations := NewRevocationSet()
	gw, err := New(Options{
		Principals:  st,
		Leases:      st,
		Revocations: revocations,
		Credentials: &fakeCredentials{keys: map[string]string{"openai": testVendor, "anthropic": testVendor}},
		Registry:    registry,
		Recorder:    NewRecorder(st, book, budget, logger),
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}

	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)

	return &recordingHarness{
		gateway: srv, upstream: up, store: st, logs: logs, principal: principal, revocations: revocations,
	}
}

// call sends an authenticated request and drains the response.
func (h *recordingHarness) call(t *testing.T, path, body string, headers map[string]string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp
}

// entries waits for the expected number of ledger rows and returns them.
//
// Accounting happens as the response body is closed, which can trail the
// client's last read, so this polls rather than assuming.
func (h *recordingHarness) entries(t *testing.T, want int) []ledger.Entry {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := h.store.LedgerEntries(context.Background(), store.LedgerFilter{})
		if err != nil {
			t.Fatalf("reading the ledger: %v", err)
		}
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ledger has %d entries, want %d\nlogs:\n%s", len(got), want, h.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------

// TestRecordsExactUsageFromVendor is the core of observe mode: a completed
// request produces a ledger entry priced from the counts the vendor reported.
func TestRecordsExactUsageFromVendor(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_vendor_123")
		_, _ = io.WriteString(w, `{
			"id": "chatcmpl-1",
			"choices": [{"message": {"content": "hi"}}],
			"usage": {"prompt_tokens": 1200, "completion_tokens": 800}
		}`)
	})

	h.call(t, "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`, nil)

	entries := h.entries(t, 1)
	e := entries[0]

	if e.Provider != "openai" || e.Model != "gpt-4o" {
		t.Errorf("entry = %s/%s, want openai/gpt-4o", e.Provider, e.Model)
	}
	if e.InputTokens != 1200 || e.OutputTokens != 800 {
		t.Errorf("tokens = %d in / %d out, want 1200/800", e.InputTokens, e.OutputTokens)
	}
	if e.HashVersion != ledger.HashVersionUsage || e.Usage[billing.UnitInputTokens] != 1200 ||
		e.Usage[billing.UnitOutputTokens] != 800 || e.ExternalID != "req_vendor_123" {
		t.Errorf("itemized usage/provenance missing: %+v", e)
	}
	if e.PricingRevision == "" || e.PriceEffective.IsZero() {
		t.Errorf("pricing provenance missing: revision=%q effective=%s", e.PricingRevision, e.PriceEffective)
	}
	// gpt-4o: 1200 * 2.50/1M + 800 * 10.00/1M = 0.003 + 0.008
	if want := money.MustParseUSD("0.011"); e.Cost != want {
		t.Errorf("cost = %s, want %s", e.Cost, want)
	}
	if e.Estimated {
		t.Error("an entry built from vendor-reported usage is marked estimated")
	}
	if e.RunID == "" || e.PrincipalID == "" {
		t.Errorf("entry is not attributed: run=%q principal=%q", e.RunID, e.PrincipalID)
	}
	if err := ledger.VerifyChain(entries); err != nil {
		t.Errorf("chain does not verify: %v", err)
	}
}

// TestRecordsStreamedUsage covers the harder path: usage read out of a live
// SSE stream without buffering it.
func TestRecordsStreamedUsage(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)

		// Anthropic reports input on message_start and a running output count
		// on each message_delta.
		fmt.Fprint(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":500,"output_tokens":1}}}

`)
		f.Flush()
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"chunk %d\"}}\n\n", i)
			f.Flush()
		}
		fmt.Fprint(w, `event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":250}}

`)
		f.Flush()
	})

	h.call(t, "/v1/messages", `{"model":"claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`, nil)

	e := h.entries(t, 1)[0]

	if e.Provider != "anthropic" || e.Model != "claude-sonnet-5" {
		t.Errorf("entry = %s/%s", e.Provider, e.Model)
	}
	if e.InputTokens != 500 || e.OutputTokens != 250 {
		t.Errorf("tokens = %d in / %d out, want 500/250 merged across events", e.InputTokens, e.OutputTokens)
	}
	if e.Estimated {
		t.Error("a streamed Anthropic response reports exact usage and must not be marked estimated")
	}
	// Sonnet 5 introductory: 500 * 2.00/1M + 250 * 10.00/1M = 0.001 + 0.0025
	if want := money.MustParseUSD("0.0035"); e.Cost != want {
		t.Errorf("cost = %s, want %s", e.Cost, want)
	}
}

// TestStreamingStillStreamsWhileRecording is the guard that accounting did not
// reintroduce buffering. This is the property phase 3 established, and the
// observing reader is the thing most likely to break it.
func TestStreamingStillStreamsWhileRecording(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)

		fmt.Fprint(w, "data: {\"type\":\"first\"}\n\n")
		f.Flush()
		<-release
		fmt.Fprint(w, `data: {"type":"message_delta","usage":{"output_tokens":10}}`+"\n\n")
		f.Flush()
	})

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-5","stream":true}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	first := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := resp.Body.Read(buf)
		if err != nil {
			first <- "error: " + err.Error()
			return
		}
		first <- string(buf[:n])
	}()

	select {
	case got := <-first:
		if !strings.Contains(got, "first") {
			t.Errorf("first chunk = %q", got)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("the first chunk did not arrive while the upstream was open: accounting reintroduced buffering")
	}
	close(release)
	_, _ = io.ReadAll(resp.Body)
}

// TestEstimatesWhenVendorReportsNoUsage covers a vendor that reports no usage
// even after being asked.
//
// spendlease now injects stream_options on OpenAI-compatible streams, so this
// upstream is standing in for one that ignores it: an OpenAI-compatible
// gateway, a proxy, or a future API change. The entry must fall back to an
// estimate and say why rather than recording nothing.
func TestEstimatesWhenVendorReportsNoUsage(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n")
			f.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	})

	h.call(t, "/v1/chat/completions",
		`{"model":"gpt-4o","stream":true,"max_tokens":500,"messages":[{"role":"user","content":"`+
			strings.Repeat("word ", 100)+`"}]}`, nil)

	e := h.entries(t, 1)[0]

	if !e.Estimated {
		t.Error("an entry with no vendor-reported usage must be marked estimated")
	}
	if e.OutputTokens != 500 {
		t.Errorf("output = %d, want the request's own max_tokens ceiling of 500", e.OutputTokens)
	}
	if e.InputTokens <= 0 {
		t.Errorf("input = %d, want a positive estimate from the prompt", e.InputTokens)
	}
	if e.Cost.IsZero() {
		t.Error("an estimated entry cost nothing")
	}
	if !strings.Contains(h.logs.String(), "vendor did not report usage") {
		t.Error("the log does not explain why the entry is estimated")
	}
}

// TestFailedUpstreamIsNotCharged is the rule that vendors do not bill for
// failures, so neither does the ledger.
func TestFailedUpstreamIsNotCharged(t *testing.T) {
	t.Parallel()

	for _, status := range []int{400, 429, 500} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			t.Parallel()

			h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"nope"},"usage":{"prompt_tokens":999,"completion_tokens":999}}`)
			})

			h.call(t, "/v1/chat/completions", `{"model":"gpt-4o"}`, nil)

			// Give accounting a chance to run, then assert it did not.
			time.Sleep(200 * time.Millisecond)
			got, err := h.store.LedgerEntries(context.Background(), store.LedgerFilter{})
			if err != nil {
				t.Fatalf("reading the ledger: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("a %d response produced %d ledger entries; failures must not be charged", status, len(got))
			}
		})
	}
}

// TestUnknownModelIsRecordedAsEstimated covers the rule that an unpriced model
// still costs money and must not be invisible.
func TestUnknownModelIsRecordedAsEstimated(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1000,"completion_tokens":1000}}`)
	})

	h.call(t, "/v1/chat/completions", `{"model":"gpt-99-unreleased"}`, nil)

	e := h.entries(t, 1)[0]

	if e.Model != "gpt-99-unreleased" {
		t.Errorf("model = %q", e.Model)
	}
	if !e.Estimated {
		t.Error("an unpriced model must be marked estimated")
	}
	if e.Cost.IsZero() {
		t.Fatal("an unpriced model cost nothing; a loop against it would be invisible")
	}
}

// TestAttributionRollsUp is what the whole product is for: spend has to be
// answerable per run and per principal.
func TestAttributionRollsUp(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1000,"completion_tokens":1000}}`)
	})

	for i := 0; i < 3; i++ {
		h.call(t, "/v1/chat/completions", `{"model":"gpt-4o"}`, nil)
	}
	entries := h.entries(t, 3)

	ctx := context.Background()
	principalID := entries[0].PrincipalID
	runID := entries[0].RunID

	// 1000 * 2.50/1M + 1000 * 10.00/1M = 0.0125 each
	want := money.MustParseUSD("0.0375")

	if got, _ := h.store.SpendByPrincipal(ctx, principalID); got != want {
		t.Errorf("SpendByPrincipal = %s, want %s", got, want)
	}
	if got, _ := h.store.SpendByRun(ctx, runID); got != want {
		t.Errorf("SpendByRun = %s, want %s", got, want)
	}
	if err := ledger.VerifyChain(entries); err != nil {
		t.Errorf("chain does not verify across three requests: %v", err)
	}
}

// TestImplicitRunIsStableAndReused checks that a principal's spend does not
// scatter across a new run per request.
func TestImplicitRunIsStableAndReused(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":10}}`)
	})

	for i := 0; i < 4; i++ {
		h.call(t, "/v1/chat/completions", `{"model":"gpt-4o"}`, nil)
	}
	entries := h.entries(t, 4)

	runID := entries[0].RunID
	for _, e := range entries {
		if e.RunID != runID {
			t.Fatalf("entries scattered across runs: %s and %s", runID, e.RunID)
		}
	}

	run, err := h.store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("the implicit run was not persisted: %v", err)
	}
	if run.PrincipalID != entries[0].PrincipalID {
		t.Error("the implicit run belongs to a different principal")
	}
	if run.Budget != money.MustParseUSD("10.00") {
		t.Errorf("implicit run budget = %s, want the configured default", run.Budget)
	}
}

// TestExplicitRunHeader covers attributing spend to a caller-created run.
func TestExplicitRunHeader(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":100}}`)
	})

	ctx := context.Background()
	principals, err := h.store.ListPrincipals(ctx)
	if err != nil || len(principals) != 1 {
		t.Fatalf("listing principals: %v", err)
	}

	mine := store.Run{
		ID: store.NewRunID(), PrincipalID: principals[0].ID,
		Budget: money.MustParseUSD("5.00"), Status: store.RunActive, CreatedAt: time.Now(),
	}
	if err := h.store.CreateRun(ctx, mine); err != nil {
		t.Fatalf("creating a run: %v", err)
	}

	h.call(t, "/v1/chat/completions", `{"model":"gpt-4o"}`, map[string]string{RunHeader: mine.ID})

	e := h.entries(t, 1)[0]
	if e.RunID != mine.ID {
		t.Errorf("entry charged to %s, want the run named in the header (%s)", e.RunID, mine.ID)
	}
}

// TestUnknownRunIsRejected checks that a bad run header fails fast rather than
// silently charging somewhere else.
func TestUnknownRunIsRejected(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	})

	resp := h.call(t, "/v1/chat/completions", `{"model":"gpt-4o"}`,
		map[string]string{RunHeader: "run_does_not_exist"})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Spendlease-Error"); got != ErrTypeUnknownRun {
		t.Errorf("error type = %q, want %q", got, ErrTypeUnknownRun)
	}
}

// TestObserveModeBlocksNothing is the promise that makes observe mode
// installable: whatever is recorded, nothing is refused.
func TestObserveModeBlocksNothing(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Far more than the run's $10 budget in a single call.
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":100000000,"completion_tokens":100000000}}`)
	})

	resp := h.call(t, "/v1/chat/completions", `{"model":"o1-pro"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: observe mode must not block", resp.StatusCode)
	}

	e := h.entries(t, 1)[0]
	if e.Cost < money.MustParseUSD("10.00") {
		t.Fatalf("the test did not exceed the budget: cost %s", e.Cost)
	}

	// And the next request is still served.
	if resp := h.call(t, "/v1/chat/completions", `{"model":"gpt-4o"}`, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("a request after exceeding the budget returned %d; observe mode must not block", resp.StatusCode)
	}
}

// TestRequestBodyReachesUpstreamUnchanged guards the replay: reading the body
// to measure it must not alter what the vendor receives.
func TestRequestBodyReachesUpstreamUnchanged(t *testing.T) {
	t.Parallel()

	const body = `{"model":"gpt-4o","messages":[{"role":"user","content":"exact body text"}],"temperature":0.7}`

	got := make(chan string, 1)
	h := newRecordingHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":10}}`)
	})

	h.call(t, "/v1/chat/completions", body, nil)

	select {
	case upstream := <-got:
		if upstream != body {
			t.Errorf("upstream received:\n%s\nwant:\n%s", upstream, body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream never received the request")
	}
}

// TestMalformedBodyIsStillProxied checks the forgiving path: a body that
// cannot be parsed must not stop the request.
func TestMalformedBodyIsStillProxied(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})

	resp := h.call(t, "/v1/chat/completions", `this is not json at all`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want the request to be proxied anyway", resp.StatusCode)
	}

	// Nothing identifiable to price, so nothing is recorded rather than
	// something wrong being recorded.
	time.Sleep(200 * time.Millisecond)
	got, _ := h.store.LedgerEntries(context.Background(), store.LedgerFilter{})
	if len(got) != 0 {
		t.Errorf("an unparseable request produced %d ledger entries", len(got))
	}
}

// TestLedgerEntryJSONShape is a small guard that the recorded fields are the
// ones the dashboard will need.
func TestLedgerEntryJSONShape(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":50}}`)
	})
	h.call(t, "/v1/chat/completions", `{"model":"gpt-4o"}`, nil)

	e := h.entries(t, 1)[0]
	raw, err := json.Marshal(map[string]any{
		"seq": e.Seq, "run": e.RunID, "principal": e.PrincipalID,
		"provider": e.Provider, "model": e.Model,
		"input": e.InputTokens, "output": e.OutputTokens,
		"cost": e.Cost.String(), "estimated": e.Estimated,
	})
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	for _, want := range []string{`"provider":"openai"`, `"model":"gpt-4o"`, `"cost":"0.00075"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("entry JSON %s does not contain %s", raw, want)
		}
	}
}
