package gateway

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/store"
)

// syncBuffer is a bytes.Buffer safe for the logger to write to while the test
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// newLoggingHarness builds a gateway whose logs the test can inspect.
func newLoggingHarness(t *testing.T, upstream http.HandlerFunc) (*httptest.Server, *syncBuffer) {
	t.Helper()

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

	logs := &syncBuffer{}
	gw, err := New(Options{
		Principals: &fakePrincipals{principal: store.Principal{
			ID: "prn_logtest", Name: "log-agent", Mode: store.ModeObserve,
		}},
		Credentials: &fakeCredentials{keys: map[string]string{"openai": testVendor, "anthropic": testVendor}},
		Registry:    registry,
		Logger:      slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}

	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return srv, logs
}

// TestRequestLogCarriesAttribution is the regression test for a bug found by
// running the binary rather than by the test suite: the log line had no
// principal or provider, because each middleware layer replaces the request
// and the outer logger never saw the inner context.
//
// Attribution is the point of this product. A request log without it is not
// worth writing.
func TestRequestLogCarriesAttribution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		path         string
		wantProvider string
	}{
		{name: "openai request", path: "/v1/chat/completions", wantProvider: "openai"},
		{name: "anthropic request", path: "/v1/messages", wantProvider: "anthropic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A harness per subtest, so "the last request log" is
			// unambiguously this subtest's.
			srv, logs := newLoggingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			})

			req, err := http.NewRequest(http.MethodPost, srv.URL+tt.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+testKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			line := lastRequestLog(t, logs)
			for _, want := range []string{
				"principal=prn_logtest",
				"provider=" + tt.wantProvider,
				"status=200",
				"method=POST",
			} {
				if !strings.Contains(line, want) {
					t.Errorf("log line %q does not contain %q", line, want)
				}
			}
		})
	}
}

// TestRequestLogNeverContainsSecrets is the enforcement of the promise in
// SECURITY.md that key material and request bodies stay out of the logs.
func TestRequestLogNeverContainsSecrets(t *testing.T) {
	t.Parallel()

	const prompt = "this-prompt-text-is-user-data"

	srv, logs := newLoggingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"a-response-body"}}]}`)
	})

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"messages":[{"content":"`+prompt+`"}]}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	got := logs.String()
	for _, forbidden := range []string{
		testKey,    // the caller's spendlease key
		testVendor, // the vendor credential
		prompt,     // the request body
		"a-response-body",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("logs contain %q, which must never be logged", forbidden)
		}
	}
}

// TestLogLevelMatchesOutcome checks that failures are findable: an operator
// filtering on warnings should see rejected requests.
func TestLogLevelMatchesOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		authorize bool
		path      string
		wantLevel string
	}{
		{name: "success is INFO", authorize: true, path: "/v1/chat/completions", wantLevel: "level=INFO"},
		{name: "rejected request is WARN", authorize: false, path: "/v1/chat/completions", wantLevel: "level=WARN"},
		{name: "unknown route is WARN", authorize: true, path: "/v2/nope", wantLevel: "level=WARN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, logs := newLoggingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{}`)
			})

			req, err := http.NewRequest(http.MethodPost, srv.URL+tt.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			if tt.authorize {
				req.Header.Set("Authorization", "Bearer "+testKey)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if line := lastRequestLog(t, logs); !strings.Contains(line, tt.wantLevel) {
				t.Errorf("log line %q is not %s", line, tt.wantLevel)
			}
		})
	}
}

// TestStreamedResponsesAreMarked checks the flag an operator would use to tell
// a streaming call from a buffered one.
//
// A short non-streaming response must NOT be marked, which is the half that
// was wrong at first: a single end-of-response flush was being reported as
// streaming, so every 401 looked like a stream.
func TestStreamedResponsesAreMarked(t *testing.T) {
	t.Parallel()

	t.Run("a short response is not marked as streamed", func(t *testing.T) {
		t.Parallel()

		srv, logs := newLoggingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"ok":true}`)
		})

		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+testKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.ReadAll(resp.Body)

		if line := lastRequestLog(t, logs); strings.Contains(line, "streamed=true") {
			t.Errorf("a non-streaming response was marked as streamed: %q", line)
		}
	})

	srv, logs := newLoggingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "data: chunk\n\n")
			f.Flush()
		}
	})

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	if line := lastRequestLog(t, logs); !strings.Contains(line, "streamed=true") {
		t.Errorf("log line %q does not mark the response as streamed", line)
	}
}

// lastRequestLog returns the most recent `msg=request` line, waiting for it to
// appear.
//
// The wait is necessary rather than defensive: the client's Do returns as soon
// as the response is readable, but the server logs after its handler returns.
// Reading the buffer immediately is a race that fails intermittently.
func lastRequestLog(t *testing.T, logs *syncBuffer) string {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var last string
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "msg=request") {
				last = line
			}
		}
		if last != "" {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("no request log line was emitted within the deadline; logs were:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
