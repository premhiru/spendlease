package gateway

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/store"
)

type observerCall struct {
	status int
	kind   string
}

type testObserver struct {
	mu    sync.Mutex
	calls []observerCall
}

func (o *testObserver) ObserveRequest(_ string, _ string, status int, _ time.Duration, _ int64) {
	o.mu.Lock()
	o.calls = append(o.calls, observerCall{status: status})
	o.mu.Unlock()
}

func (*testObserver) ObserveBudget(string, string, string) {}

func (o *testObserver) Notify(kind string, _ map[string]string) {
	o.mu.Lock()
	o.calls = append(o.calls, observerCall{kind: kind})
	o.mu.Unlock()
}

func (o *testObserver) contains(want observerCall) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, call := range o.calls {
		if (want.status == 0 || call.status == want.status) && (want.kind == "" || call.kind == want.kind) {
			return true
		}
	}
	return false
}

func productionGateway(t *testing.T, transport http.RoundTripper, observer Observer, maxInFlight int, timeout time.Duration) *Gateway {
	t.Helper()
	provider, err := openai.NewWithBaseURL("https://vendor.example")
	if err != nil {
		t.Fatalf("openai provider: %v", err)
	}
	registry, err := providers.NewRegistry(provider)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	gw, err := New(Options{
		Principals:  &fakePrincipals{principal: store.Principal{ID: "prn_test", Mode: store.ModeObserve}},
		Credentials: &fakeCredentials{keys: map[string]string{"openai": testVendor}},
		Registry:    registry, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Transport: transport, Observer: observer, MaxInFlight: maxInFlight, UpstreamTimeout: timeout,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return gw
}

func authenticatedRequest(t *testing.T, client *http.Client, baseURL string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-4o","stream":false}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestMaxInFlightFailsFastAndLeavesOperationsReachable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})
	observer := &testObserver{}
	server := httptest.NewServer(productionGateway(t, transport, observer, 1, time.Minute).Handler())
	defer server.Close()

	firstRequest, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-4o","stream":false}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	firstRequest.Header.Set("Authorization", "Bearer "+testKey)
	type requestResult struct {
		response *http.Response
		err      error
	}
	firstDone := make(chan requestResult, 1)
	go func() {
		response, requestErr := server.Client().Do(firstRequest)
		firstDone <- requestResult{response: response, err: requestErr}
	}()
	<-started
	second := authenticatedRequest(t, server.Client(), server.URL)
	defer func() { _ = second.Body.Close() }()
	if second.StatusCode != http.StatusServiceUnavailable || second.Header.Get("Retry-After") != "1" {
		t.Fatalf("limited response = %d, Retry-After %q", second.StatusCode, second.Header.Get("Retry-After"))
	}
	if !observer.contains(observerCall{status: http.StatusServiceUnavailable}) {
		t.Fatal("observer did not record the concurrency rejection")
	}
	health, err := server.Client().Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health under load = %d", health.StatusCode)
	}
	close(release)
	first := <-firstDone
	if first.err != nil {
		t.Fatalf("first request: %v", first.err)
	}
	_ = first.response.Body.Close()
}

func TestNonStreamingUpstreamTimeout(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	observer := &testObserver{}
	server := httptest.NewServer(productionGateway(t, transport, observer, 0, 25*time.Millisecond).Handler())
	defer server.Close()
	started := time.Now()
	resp := authenticatedRequest(t, server.Client(), server.URL)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("timeout status = %d, want 502", resp.StatusCode)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("upstream timeout took %s", elapsed)
	}
	if !observer.contains(observerCall{kind: "upstream_error"}) {
		t.Fatal("observer did not receive the upstream timeout alert")
	}
}
