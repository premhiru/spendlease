package observability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type readyStore struct{ err error }

func (s readyStore) PingContext(context.Context) error { return s.err }

func newTestService(t *testing.T, store ReadyStore) *Service {
	t.Helper()
	service, err := New(Options{Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return service
}

func TestReadinessReflectsDatastore(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
		want int
	}{
		{name: "ready", want: http.StatusOK},
		{name: "database unavailable", err: errors.New("offline"), want: http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := newTestService(t, readyStore{err: tt.err})
			mux := http.NewServeMux()
			service.Routes(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestMetricsUseBoundedLabels(t *testing.T) {
	t.Parallel()
	service := newTestService(t, readyStore{})
	service.ObserveRequest("/openai/v1/chat/completions", "openai", 200, 1500*time.Millisecond, 42)
	service.ObserveBudget("openai", "enforce", "blocked")
	rec := httptest.NewRecorder()
	service.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`spendlease_build_info{version="test"} 1`,
		`spendlease_http_requests_total{surface="proxy",status_class="2xx",provider="openai"} 1`,
		`spendlease_http_request_duration_seconds_sum{surface="proxy",status_class="2xx",provider="openai"} 1.500000`,
		`spendlease_budget_decisions_total{provider="openai",mode="enforce",outcome="blocked"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestWebhookIsSignedAndDrained(t *testing.T) {
	var body []byte
	var signature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		signature = r.Header.Get("X-Spendlease-Signature")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	const secret = "webhook-secret"
	service, err := New(Options{
		Store: readyStore{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test",
		WebhookURL: server.URL, WebhookSecret: secret,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	service.Notify("budget_blocked", map[string]string{"principal": "prn_a"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if signature != want || !strings.Contains(string(body), `"type":"budget_blocked"`) {
		t.Fatalf("signature/body = %q / %s", signature, body)
	}
	// Late notifications are ignored rather than panicking on the closed queue.
	service.Notify("late", nil)
}

func TestWebhookURLValidation(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"ftp://example.com/hook", "https://user:pass@example.com/hook", "://bad"} {
		if _, err := New(Options{
			Store: readyStore{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), WebhookURL: raw,
		}); err == nil {
			t.Errorf("New accepted webhook URL %q", raw)
		}
	}
}
