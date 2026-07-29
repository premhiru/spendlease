package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
)

// streamingUpstream replies with an SSE stream, optionally honouring
// stream_options the way OpenAI does.
func streamingUpstream(t *testing.T, gate <-chan struct{}) (http.HandlerFunc, <-chan map[string]any) {
	t.Helper()

	seen := make(chan map[string]any, 4)

	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		seen <- body

		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)

		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d\"}}]}\n\n", i)
			f.Flush()
			if gate != nil {
				<-gate
			}
		}

		// Honour include_usage exactly as OpenAI does.
		if opts, ok := body["stream_options"].(map[string]any); ok {
			if include, _ := opts["include_usage"].(bool); include {
				fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":1500,"completion_tokens":640}}`+"\n\n")
				f.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}, seen
}

// TestInjectsStreamOptions is the behaviour the phase brief calls for: an
// OpenAI-compatible streaming request that does not ask for usage is modified
// so that it does, because otherwise the call cannot be priced exactly.
func TestInjectsStreamOptions(t *testing.T) {
	t.Parallel()

	handler, seen := streamingUpstream(t, nil)
	h := newRecordingHarness(t, handler)

	h.call(t, "/v1/chat/completions",
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)

	select {
	case body := <-seen:
		opts, ok := body["stream_options"].(map[string]any)
		if !ok {
			t.Fatalf("the upstream received no stream_options: %v", body)
		}
		if include, _ := opts["include_usage"].(bool); !include {
			t.Errorf("include_usage = %v, want true", opts["include_usage"])
		}
		// The rest of the request must be untouched.
		if body["model"] != "gpt-4o" || body["stream"] != true {
			t.Errorf("injection altered other fields: %v", body)
		}
		if _, ok := body["messages"].([]any); !ok {
			t.Error("injection lost the messages")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream never received the request")
	}

	// And the entry is now exact rather than estimated.
	e := h.entries(t, 1)[0]
	if e.Estimated {
		t.Error("the entry is still estimated; injection did not achieve exact accounting")
	}
	if e.InputTokens != 1500 || e.OutputTokens != 640 {
		t.Errorf("tokens = %d/%d, want the vendor-reported 1500/640", e.InputTokens, e.OutputTokens)
	}
	if want := money.MustParseUSD("0.01015"); e.Cost != want {
		t.Errorf("cost = %s, want %s", e.Cost, want)
	}
}

// TestInjectedUsageChunkIsWithheld is the other half of the bargain: having
// asked for usage on the caller's behalf, the extra chunk must not appear in
// the stream they read.
func TestInjectedUsageChunkIsWithheld(t *testing.T) {
	t.Parallel()

	handler, _ := streamingUpstream(t, nil)
	h := newRecordingHarness(t, handler)

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	got := string(body)

	if strings.Contains(got, "usage") {
		t.Errorf("the caller's stream contains the injected usage chunk:\n%s", got)
	}
	// Everything the caller expected is still there.
	for _, want := range []string{"tok0", "tok1", "tok2", "[DONE]"} {
		if !strings.Contains(got, want) {
			t.Errorf("the filtered stream is missing %q:\n%s", want, got)
		}
	}

	// And the modification is discoverable.
	if h := resp.Header.Get(StreamUsageHeader); h != "injected" {
		t.Errorf("%s = %q, want \"injected\": a modified request must announce itself",
			StreamUsageHeader, h)
	}
}

// TestCallerRequestedUsageIsPassedThrough covers the case where the caller
// asked for usage themselves. Nothing is injected, nothing is withheld, and
// the header does not appear.
func TestCallerRequestedUsageIsPassedThrough(t *testing.T) {
	t.Parallel()

	handler, seen := streamingUpstream(t, nil)
	h := newRecordingHarness(t, handler)

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":true},"messages":[]}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "usage") {
		t.Error("the caller asked for usage and did not receive it")
	}
	if got := resp.Header.Get(StreamUsageHeader); got != "" {
		t.Errorf("%s = %q, want absent when nothing was injected", StreamUsageHeader, got)
	}

	<-seen // drain

	if e := h.entries(t, 1)[0]; e.Estimated {
		t.Error("usage was reported, so the entry should be exact")
	}
}

// TestAnthropicIsNeverModified guards the rule that a vendor which already
// reports usage is left alone.
func TestAnthropicIsNeverModified(t *testing.T) {
	t.Parallel()

	seen := make(chan string, 1)
	h := newRecordingHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)

		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, `data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1}}}`+"\n\n")
		f.Flush()
		fmt.Fprint(w, `data: {"type":"message_delta","usage":{"output_tokens":50}}`+"\n\n")
		f.Flush()
	})

	const body = `{"model":"claude-sonnet-5","stream":true,"max_tokens":100,"messages":[]}`
	resp := h.call(t, "/v1/messages", body, nil)

	select {
	case got := <-seen:
		if got != body {
			t.Errorf("Anthropic's request was modified:\ngot  %s\nwant %s", got, body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream never received the request")
	}

	if got := resp.Header.Get(StreamUsageHeader); got != "" {
		t.Errorf("%s = %q, want absent for Anthropic", StreamUsageHeader, got)
	}
}

// TestNonStreamingIsNeverModified checks the injection is confined to streams.
func TestNonStreamingIsNeverModified(t *testing.T) {
	t.Parallel()

	seen := make(chan string, 1)
	h := newRecordingHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":10}}`)
	})

	const body = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	h.call(t, "/v1/chat/completions", body, nil)

	select {
	case got := <-seen:
		if got != body {
			t.Errorf("a non-streaming request was modified:\ngot  %s\nwant %s", got, body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream never received the request")
	}
}

// TestFilteredStreamStillStreams is the safety check on the filtering path.
//
// Withholding an event means holding each one until it is complete, which is
// the one place this change could have reintroduced buffering. The upstream
// blocks between chunks, so a buffering filter would deadlock this test.
func TestFilteredStreamStillStreams(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	handler, _ := streamingUpstream(t, gate)
	h := newRecordingHarness(t, handler)

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := bufio.NewReader(resp.Body)

	for i := 0; i < 3; i++ {
		line := make(chan string, 1)
		go func() {
			s, err := reader.ReadString('\n')
			if err != nil {
				line <- "error: " + err.Error()
				return
			}
			line <- s
		}()

		select {
		case got := <-line:
			want := fmt.Sprintf("tok%d", i)
			if !strings.Contains(got, want) {
				t.Fatalf("chunk %d = %q, want %q", i, got, want)
			}
		case <-time.After(3 * time.Second):
			close(gate)
			t.Fatalf("chunk %d never arrived: the filtering path is buffering", i)
		}

		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatalf("reading the blank line after chunk %d: %v", i, err)
		}
		gate <- struct{}{}
	}
	close(gate)
}

// TestUsageChunkSplitAcrossReadsIsStillWithheld covers the boundary case the
// filter is most likely to get wrong: an event arriving in pieces.
func TestUsageChunkSplitAcrossReadsIsStillWithheld(t *testing.T) {
	t.Parallel()

	h := newRecordingHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)

		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		f.Flush()

		// Dribble the usage chunk out a few bytes at a time.
		chunk := `data: {"choices":[],"usage":{"prompt_tokens":1500,"completion_tokens":640}}` + "\n\n"
		for i := 0; i < len(chunk); i += 7 {
			end := i + 7
			if end > len(chunk) {
				end = len(chunk)
			}
			fmt.Fprint(w, chunk[i:end])
			f.Flush()
			time.Sleep(2 * time.Millisecond)
		}
	})

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "usage") {
		t.Errorf("a usage chunk split across reads leaked to the caller:\n%s", body)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("the content chunk was lost:\n%s", body)
	}

	// It was still counted.
	if e := h.entries(t, 1)[0]; e.InputTokens != 1500 || e.OutputTokens != 640 {
		t.Errorf("tokens = %d/%d, want 1500/640 read from the split chunk", e.InputTokens, e.OutputTokens)
	}
}
