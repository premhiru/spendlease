package gateway

import (
	"bufio"
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

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/vault"
)

const (
	testKey    = "slk_testprincipalkey"
	testVendor = "sk-vendor-secret"
)

// fakePrincipals resolves exactly one key.
type fakePrincipals struct {
	principal store.Principal
	err       error
}

func (f *fakePrincipals) GetPrincipalByKeyHash(_ context.Context, hash string) (store.Principal, error) {
	if f.err != nil {
		return store.Principal{}, f.err
	}
	if hash != store.HashSecret(testKey) {
		return store.Principal{}, store.ErrNotFound
	}
	return f.principal, nil
}

// fakeCredentials serves vendor keys from a map.
type fakeCredentials struct {
	keys map[string]string
	err  error
}

func (f *fakeCredentials) Get(_ context.Context, provider string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	k, ok := f.keys[provider]
	if !ok {
		return "", fmt.Errorf("%w: %s", vault.ErrNoCredential, provider)
	}
	return k, nil
}

// harness is a gateway wired to a fake upstream.
type harness struct {
	gateway  *httptest.Server
	upstream *httptest.Server
	// lastUpstream captures what the upstream actually received.
	lastUpstream *http.Request
}

// newHarness builds a gateway in front of the given upstream handler.
func newHarness(t *testing.T, upstream http.HandlerFunc, creds map[string]string) *harness {
	t.Helper()

	h := &harness{}
	h.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(context.Background())
		h.lastUpstream = clone
		upstream(w, r)
	}))
	t.Cleanup(h.upstream.Close)

	oa, err := openai.NewWithBaseURL(h.upstream.URL)
	if err != nil {
		t.Fatalf("openai adapter: %v", err)
	}
	an, err := anthropic.NewWithBaseURL(h.upstream.URL)
	if err != nil {
		t.Fatalf("anthropic adapter: %v", err)
	}
	registry, err := providers.NewRegistry(oa, an)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	if creds == nil {
		creds = map[string]string{"openai": testVendor, "anthropic": testVendor}
	}

	gw, err := New(Options{
		Principals: &fakePrincipals{principal: store.Principal{
			ID: "prn_test", Name: "test-agent", Mode: store.ModeObserve,
		}},
		Credentials: &fakeCredentials{keys: creds},
		Registry:    registry,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}

	h.gateway = httptest.NewServer(gw.Handler())
	t.Cleanup(h.gateway.Close)
	return h
}

// do sends an authenticated request through the gateway.
func (h *harness) do(t *testing.T, method, path string, body io.Reader) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, h.gateway.URL+path, body)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

// TestStreamingIsNotBuffered is the headline guarantee of this phase.
//
// The upstream sends one SSE chunk, then blocks until the test says otherwise.
// If the proxy buffers, the client's first read cannot return and the test
// deadlocks until its timeout. Passing means chunks genuinely reach the client
// as they are produced, which is what first-token latency depends on.
func TestStreamingIsNotBuffered(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	upstreamDone := make(chan struct{})

	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		defer close(upstreamDone)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream ResponseWriter is not a Flusher")
			return
		}

		_, _ = io.WriteString(w, "data: {\"chunk\":1}\n\n")
		flusher.Flush()

		// Hold the response open. A buffering proxy cannot deliver the first
		// chunk until this returns.
		<-release

		_, _ = io.WriteString(w, "data: {\"chunk\":2}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}, nil)

	resp := h.do(t, http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true}`))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	reader := bufio.NewReader(resp.Body)
	firstChunk := make(chan string, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			firstChunk <- "error: " + err.Error()
			return
		}
		firstChunk <- line
	}()

	select {
	case got := <-firstChunk:
		if !strings.Contains(got, `"chunk":1`) {
			t.Errorf("first line = %q, want the first data chunk", got)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("first chunk did not arrive while the upstream was still open: the proxy is buffering")
	}

	// The first chunk arrived before the upstream finished, which is the
	// property under test. Let the upstream complete.
	close(release)
	<-upstreamDone

	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the rest of the stream: %v", err)
	}
	for _, want := range []string{`"chunk":2`, "[DONE]"} {
		if !strings.Contains(string(rest), want) {
			t.Errorf("remainder %q does not contain %q", rest, want)
		}
	}
}

// TestStreamChunksArriveIndividually checks that chunks are delivered one at a
// time rather than coalesced, which is what an agent consuming tokens needs.
func TestStreamChunksArriveIndividually(t *testing.T) {
	t.Parallel()

	const chunks = 5
	proceed := make(chan struct{}, chunks)

	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 1; i <= chunks; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			flusher.Flush()
			// Wait for the reader to confirm it saw this chunk.
			<-proceed
		}
	}, nil)

	resp := h.do(t, http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	reader := bufio.NewReader(resp.Body)

	for i := 1; i <= chunks; i++ {
		got := make(chan string, 1)
		go func() {
			line, err := reader.ReadString('\n')
			if err != nil {
				got <- "error: " + err.Error()
				return
			}
			got <- line
		}()

		select {
		case line := <-got:
			want := fmt.Sprintf("data: %d", i)
			if !strings.Contains(line, want) {
				t.Fatalf("chunk %d = %q, want %q", i, line, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("chunk %d never arrived", i)
		}

		// Consume the blank line that terminates the SSE event.
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatalf("reading the blank line after chunk %d: %v", i, err)
		}
		proceed <- struct{}{}
	}
}

// TestClientDisconnectDoesNotPanic covers the mid-stream hangup. The gateway
// must notice, stop cleanly, and not take the process down.
func TestClientDisconnectDoesNotPanic(t *testing.T) {
	t.Parallel()

	upstreamExited := make(chan struct{})

	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamExited)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 500; i++ {
			if _, err := fmt.Fprintf(w, "data: %d\n\n", i); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}, nil)

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	ctx, cancel := context.WithCancel(context.Background())
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatalf("request: %v", err)
	}

	// Read one chunk, then hang up mid-stream.
	buf := make([]byte, 16)
	if _, err := resp.Body.Read(buf); err != nil {
		cancel()
		t.Fatalf("reading first chunk: %v", err)
	}
	cancel()
	_ = resp.Body.Close()

	select {
	case <-upstreamExited:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream handler did not observe the client disconnect")
	}

	// The gateway must still serve other requests.
	if r := h.do(t, http.MethodGet, "/healthz", nil); r.StatusCode != http.StatusOK {
		t.Errorf("gateway unhealthy after a disconnect: status %d", r.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Credential handling
// ---------------------------------------------------------------------------

// TestClientCredentialIsStrippedAndVendorKeyAttached is the security core of
// the proxy: the caller's spendlease key must never reach the vendor, and the
// vendor must receive the key the gateway chose.
func TestClientCredentialIsStrippedAndVendorKeyAttached(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		clientHdrs  map[string]string
		wantHeader  string
		wantValue   string
		absentHdrs  []string
		extraAssert func(t *testing.T, got *http.Request)
	}{
		{
			name:       "openai uses a bearer token",
			path:       "/v1/chat/completions",
			wantHeader: "Authorization",
			wantValue:  "Bearer " + testVendor,
			absentHdrs: []string{"x-api-key"},
		},
		{
			name:       "anthropic uses x-api-key and no bearer",
			path:       "/v1/messages",
			wantHeader: "x-api-key",
			wantValue:  testVendor,
			absentHdrs: []string{"Authorization"},
			extraAssert: func(t *testing.T, got *http.Request) {
				if v := got.Header.Get("anthropic-version"); v == "" {
					t.Error("anthropic-version was not defaulted")
				}
			},
		},
		{
			name: "a vendor key supplied by the caller is discarded",
			path: "/v1/chat/completions",
			clientHdrs: map[string]string{
				"x-api-key": "sk-caller-supplied",
				"api-key":   "sk-also-caller-supplied",
			},
			wantHeader: "Authorization",
			wantValue:  "Bearer " + testVendor,
			absentHdrs: []string{"x-api-key", "api-key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"ok":true}`)
			}, nil)

			req, err := http.NewRequest(http.MethodPost, h.gateway.URL+tt.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+testKey)
			for k, v := range tt.clientHdrs {
				req.Header.Set(k, v)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			got := h.lastUpstream
			if got == nil {
				t.Fatal("the upstream was never reached")
			}

			if v := got.Header.Get(tt.wantHeader); v != tt.wantValue {
				t.Errorf("upstream %s = %q, want %q", tt.wantHeader, v, tt.wantValue)
			}
			for _, hdr := range tt.absentHdrs {
				if v := got.Header.Get(hdr); v != "" {
					t.Errorf("upstream received %s = %q, which should have been stripped", hdr, v)
				}
			}

			// The spendlease key must not appear anywhere in the outbound
			// headers, under any name.
			for name, values := range got.Header {
				for _, v := range values {
					if strings.Contains(v, testKey) {
						t.Errorf("the caller's spendlease key leaked upstream in header %s", name)
					}
				}
			}

			if tt.extraAssert != nil {
				tt.extraAssert(t, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

func TestAuthentication(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setHeaders func(h http.Header)
		wantStatus int
		wantType   string
		wantInMsg  string
	}{
		{
			name:       "no credential",
			setHeaders: func(http.Header) {},
			wantStatus: http.StatusUnauthorized,
			wantType:   ErrTypeUnauthenticated,
			wantInMsg:  "No spendlease credential",
		},
		{
			name:       "a vendor key instead of a spendlease key",
			setHeaders: func(h http.Header) { h.Set("Authorization", "Bearer sk-proj-abcdef") },
			wantStatus: http.StatusUnauthorized,
			wantType:   ErrTypeUnauthenticated,
			wantInMsg:  "not a spendlease key",
		},
		{
			name:       "a lease token, which is not supported yet",
			setHeaders: func(h http.Header) { h.Set("Authorization", "Bearer sll_sometoken") },
			wantStatus: http.StatusUnauthorized,
			wantType:   ErrTypeUnauthenticated,
			wantInMsg:  "lease authentication is not implemented yet",
		},
		{
			name:       "an unknown principal key",
			setHeaders: func(h http.Header) { h.Set("Authorization", "Bearer slk_wrongkey") },
			wantStatus: http.StatusUnauthorized,
			wantType:   ErrTypeUnauthenticated,
			wantInMsg:  "not recognised",
		},
		{
			name:       "a valid key via x-api-key",
			setHeaders: func(h http.Header) { h.Set("x-api-key", testKey) },
			wantStatus: http.StatusOK,
		},
		{
			name:       "a valid key with no bearer prefix",
			setHeaders: func(h http.Header) { h.Set("Authorization", testKey) },
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			}, nil)

			req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
				strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			tt.setHeaders(req.Header)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				return
			}

			assertAPIError(t, resp, tt.wantType, tt.wantInMsg)
		})
	}
}

// assertAPIError checks the structured error body.
func assertAPIError(t *testing.T, resp *http.Response, wantType, wantInMsg string) {
	t.Helper()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
	if got := resp.Header.Get("X-Spendlease-Error"); got != wantType {
		t.Errorf("X-Spendlease-Error = %q, want %q", got, wantType)
	}

	var body APIError
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding the error body: %v", err)
	}
	if body.Error.Type != wantType {
		t.Errorf("error type = %q, want %q", body.Error.Type, wantType)
	}
	if wantInMsg != "" && !strings.Contains(body.Error.Message, wantInMsg) {
		t.Errorf("message %q does not contain %q", body.Error.Message, wantInMsg)
	}
	// Errors are a product surface: every failure must tell the reader what
	// to do next.
	if body.Error.Resolution == "" {
		t.Errorf("error %q has no resolution; it does not tell the reader what to do", body.Error.Type)
	}
}

// ---------------------------------------------------------------------------
// Routing and upstream failures
// ---------------------------------------------------------------------------

func TestRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		path         string
		headers      map[string]string
		wantUpstream string
		wantStatus   int
	}{
		{name: "openai chat completions", path: "/v1/chat/completions", wantUpstream: "/v1/chat/completions", wantStatus: 200},
		{name: "openai embeddings", path: "/v1/embeddings", wantUpstream: "/v1/embeddings", wantStatus: 200},
		{name: "anthropic messages", path: "/v1/messages", wantUpstream: "/v1/messages", wantStatus: 200},
		{name: "anthropic complete", path: "/v1/complete", wantUpstream: "/v1/complete", wantStatus: 200},
		{
			name: "ambiguous path with the anthropic header goes to anthropic",
			path: "/v1/models", headers: map[string]string{"anthropic-version": "2023-06-01"},
			wantUpstream: "/v1/models", wantStatus: 200,
		},
		{name: "ambiguous path without a hint falls back", path: "/v1/models", wantUpstream: "/v1/models", wantStatus: 200},
		{name: "explicit provider prefix is stripped", path: "/anthropic/v1/models", wantUpstream: "/v1/models", wantStatus: 200},
		{name: "explicit openai prefix is stripped", path: "/openai/v1/chat/completions", wantUpstream: "/v1/chat/completions", wantStatus: 200},
		{name: "unknown path is refused", path: "/v2/unknown/thing", wantStatus: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			}, nil)

			req, err := http.NewRequest(http.MethodPost, h.gateway.URL+tt.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+testKey)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus != 200 {
				assertAPIError(t, resp, ErrTypeUnknownRoute, "")
				return
			}
			if got := h.lastUpstream.URL.Path; got != tt.wantUpstream {
				t.Errorf("upstream path = %q, want %q", got, tt.wantUpstream)
			}
		})
	}
}

// TestQueryStringIsPreserved guards a detail that is easy to drop when
// rewriting URLs and silently breaks paginated endpoints.
func TestQueryStringIsPreserved(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}, nil)

	h.do(t, http.MethodGet, "/v1/models?limit=5&after=abc", nil)

	if got := h.lastUpstream.URL.RawQuery; got != "limit=5&after=abc" {
		t.Errorf("upstream query = %q, want it preserved", got)
	}
}

// TestLargeRequestBodyIsNotTruncated protects the pass-through guarantee for
// bodies too large to inspect. The parser deliberately declines to measure
// them, but the vendor must still receive every byte.
func TestLargeRequestBodyIsNotTruncated(t *testing.T) {
	t.Parallel()

	want := bytes.Repeat([]byte("x"), maxRequestBody+257)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(want))

	measured, err := readRequestBody(req)
	if err != nil {
		t.Fatalf("readRequestBody: %v", err)
	}
	if measured != nil {
		t.Fatalf("measured %d bytes, want nil for an oversized body", len(measured))
	}

	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading replayed body: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("replayed body length = %d, want %d; request was truncated", len(got), len(want))
	}
}

// TestUpstreamErrorsArePassedThrough checks that a vendor's own error reaches
// the caller unchanged. The gateway must not rewrite a 400 from OpenAI into
// something of its own, or debugging becomes guesswork.
func TestUpstreamErrorsArePassedThrough(t *testing.T) {
	t.Parallel()

	for _, status := range []int{400, 401, 429, 500, 503} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			t.Parallel()

			body := fmt.Sprintf(`{"error":{"message":"vendor said %d"}}`, status)
			h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			}, nil)

			resp := h.do(t, http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
			if resp.StatusCode != status {
				t.Errorf("status = %d, want %d passed through", resp.StatusCode, status)
			}
			got, _ := io.ReadAll(resp.Body)
			if string(got) != body {
				t.Errorf("body = %q, want the vendor's body unchanged", got)
			}
			if resp.Header.Get("X-Spendlease-Error") != "" {
				t.Error("a vendor error was labelled as a spendlease error")
			}
		})
	}
}

// TestUnreachableUpstreamGives502 covers the vendor being down.
func TestUnreachableUpstreamGives502(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, nil)
	// Close the upstream so connections are refused.
	h.upstream.Close()

	resp := h.do(t, http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	assertAPIError(t, resp, ErrTypeUpstream, "could not reach")
}

// TestMissingVendorCredential covers the most likely first-run mistake, and
// checks the error actually says how to fix it.
func TestMissingVendorCredential(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}, map[string]string{}) // no credentials at all

	resp := h.do(t, http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	var body APIError
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if body.Error.Type != ErrTypeNoCredential {
		t.Errorf("type = %q, want %q", body.Error.Type, ErrTypeNoCredential)
	}
	if body.Error.Provider != "openai" {
		t.Errorf("provider = %q, want openai", body.Error.Provider)
	}
	if !strings.Contains(body.Error.Resolution, "keys provider set openai") {
		t.Errorf("resolution %q does not give the exact command to run", body.Error.Resolution)
	}
}

// ---------------------------------------------------------------------------
// Operational endpoints
// ---------------------------------------------------------------------------

func TestOperationalEndpointsNeedNoAuth(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, nil)

	tests := []struct {
		path       string
		wantInBody string
	}{
		{"/healthz", `"status":"ok"`},
		{"/", "spendlease"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			resp, err := http.Get(h.gateway.URL + tt.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.path, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tt.wantInBody) {
				t.Errorf("body %q does not contain %q", body, tt.wantInBody)
			}
		})
	}
}

func TestNewValidatesOptions(t *testing.T) {
	t.Parallel()

	registry, err := providers.NewRegistry(openai.New())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name string
		opts Options
	}{
		{"no principals", Options{Credentials: &fakeCredentials{}, Registry: registry, Logger: logger}},
		{"no credentials", Options{Principals: &fakePrincipals{}, Registry: registry, Logger: logger}},
		{"no registry", Options{Principals: &fakePrincipals{}, Credentials: &fakeCredentials{}, Logger: logger}},
		{"no logger", Options{Principals: &fakePrincipals{}, Credentials: &fakeCredentials{}, Registry: registry}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := New(tt.opts); err == nil {
				t.Error("New accepted incomplete options")
			}
		})
	}
}
