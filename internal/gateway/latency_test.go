//go:build !race

package gateway

import (
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/store"
)

const benchmarkStream = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
	"data: [DONE]\n\n"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func streamingBenchmarkHandler(tb testing.TB) http.Handler {
	tb.Helper()
	oa, err := openai.NewWithBaseURL("http://benchmark.invalid")
	if err != nil {
		tb.Fatal(err)
	}
	registry, err := providers.NewRegistry(oa)
	if err != nil {
		tb.Fatal(err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(benchmarkStream)),
			Request:    r,
		}, nil
	})
	gw, err := New(Options{
		Principals: &fakePrincipals{principal: store.Principal{
			ID: "prn_benchmark", Name: "benchmark", Mode: store.ModeObserve,
		}},
		Credentials: &fakeCredentials{keys: map[string]string{"openai": testVendor}},
		Registry:    registry,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Transport:   transport,
	})
	if err != nil {
		tb.Fatal(err)
	}
	return gw.Handler()
}

func streamingRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestStreamingGatewayOverheadP99 enforces the latency budget without any
// provider or network time: the transport returns an in-memory SSE response.
func TestStreamingGatewayOverheadP99(t *testing.T) {
	handler := streamingBenchmarkHandler(t)
	const samples = 300
	// Exclude one-time template, reflection and connection-path warm-up. This
	// is a steady-state gateway SLO, not a process-start benchmark.
	for range 50 {
		handler.ServeHTTP(httptest.NewRecorder(), streamingRequest())
	}
	latencies := make([]time.Duration, 0, samples)
	for range samples {
		response := httptest.NewRecorder()
		started := time.Now()
		handler.ServeHTTP(response, streamingRequest())
		latencies = append(latencies, time.Since(started))
		if response.Code != http.StatusOK {
			t.Fatalf("gateway returned %d", response.Code)
		}
	}
	p99 := percentile99(latencies)
	if p99 >= 10*time.Millisecond {
		t.Fatalf("streaming gateway overhead p99 = %s, want under 10ms", p99)
	}
	t.Logf("streaming gateway overhead p99: %s (%d in-memory samples)", p99, samples)
}

// BenchmarkStreamingGatewayOverhead measures the request-to-complete cost of
// proxying an in-memory SSE response, excluding provider and network time.
func BenchmarkStreamingGatewayOverhead(b *testing.B) {
	handler := streamingBenchmarkHandler(b)
	latencies := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		response := httptest.NewRecorder()
		started := time.Now()
		handler.ServeHTTP(response, streamingRequest())
		latencies = append(latencies, time.Since(started))
	}
	b.StopTimer()
	b.ReportMetric(float64(percentile99(latencies).Nanoseconds()), "p99-ns")
}

func percentile99(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	index := int(math.Ceil(float64(len(samples))*0.99)) - 1
	return samples[index]
}
