package providers_test

import (
	"testing"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
)

func TestParseRequestOpenAI(t *testing.T) {
	t.Parallel()

	p := openai.New()

	tests := []struct {
		name           string
		body           string
		wantModel      string
		wantMaxTokens  int64
		wantStream     bool
		wantUsage      bool
		wantCharsAbove int64
	}{
		{
			name:      "a plain chat completion",
			body:      `{"model":"gpt-4o","messages":[{"role":"user","content":"hello world"}]}`,
			wantModel: "gpt-4o", wantCharsAbove: 10,
		},
		{
			name:          "max_tokens is read",
			body:          `{"model":"gpt-4o","max_tokens":512,"messages":[]}`,
			wantModel:     "gpt-4o",
			wantMaxTokens: 512,
		},
		{
			name:          "max_completion_tokens is read for newer models",
			body:          `{"model":"o3","max_completion_tokens":2048,"messages":[]}`,
			wantModel:     "o3",
			wantMaxTokens: 2048,
		},
		{
			name:       "streaming is detected",
			body:       `{"model":"gpt-4o","stream":true,"messages":[]}`,
			wantModel:  "gpt-4o",
			wantStream: true,
		},
		{
			name:      "usage opt-in is detected",
			body:      `{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":true},"messages":[]}`,
			wantModel: "gpt-4o", wantStream: true, wantUsage: true,
		},
		{
			name:      "streaming without usage opt-in",
			body:      `{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":false},"messages":[]}`,
			wantModel: "gpt-4o", wantStream: true, wantUsage: false,
		},
		{
			name: "multipart content is counted",
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":[` +
				`{"type":"text","text":"describe this picture in detail"},` +
				`{"type":"image_url","image_url":{"url":"data:x"}}]}]}`,
			wantModel: "gpt-4o", wantCharsAbove: 25,
		},
		{name: "not json", body: `<xml/>`},
		{name: "empty", body: ``},
		{name: "json but not an object", body: `[1,2,3]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := p.ParseRequest([]byte(tt.body))
			if got.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", got.Model, tt.wantModel)
			}
			if got.MaxTokens != tt.wantMaxTokens {
				t.Errorf("max tokens = %d, want %d", got.MaxTokens, tt.wantMaxTokens)
			}
			if got.Stream != tt.wantStream {
				t.Errorf("stream = %v, want %v", got.Stream, tt.wantStream)
			}
			if got.WantsUsage != tt.wantUsage {
				t.Errorf("wants usage = %v, want %v", got.WantsUsage, tt.wantUsage)
			}
			if tt.wantCharsAbove > 0 && got.PromptChars < tt.wantCharsAbove {
				t.Errorf("prompt chars = %d, want more than %d", got.PromptChars, tt.wantCharsAbove)
			}
		})
	}
}

// TestParseRequestAnthropicAlwaysWantsUsage records the asymmetry that decides
// whether a streamed request can be priced exactly.
func TestParseRequestAnthropicAlwaysWantsUsage(t *testing.T) {
	t.Parallel()

	got := anthropic.New().ParseRequest([]byte(
		`{"model":"claude-sonnet-5","max_tokens":1024,"stream":true,` +
			`"system":"be brief","messages":[{"role":"user","content":"hello"}]}`))

	if got.Model != "claude-sonnet-5" || got.MaxTokens != 1024 || !got.Stream {
		t.Errorf("parsed = %+v", got)
	}
	if !got.WantsUsage {
		t.Error("Anthropic reports usage on every stream, so WantsUsage should be true")
	}
	// "be brief" plus "hello" is 13 characters of prompt text.
	if got.PromptChars < 13 {
		t.Errorf("prompt chars = %d, want the system prompt counted too", got.PromptChars)
	}
}

// TestPromptCharsIgnoresNonPromptText checks that identifiers and settings are
// not counted as prompt, which would inflate every estimate.
func TestPromptCharsIgnoresNonPromptText(t *testing.T) {
	t.Parallel()

	withPrompt := openai.New().ParseRequest([]byte(
		`{"model":"gpt-4o","messages":[{"role":"user","content":"abcdefghij"}]}`))
	withoutPrompt := openai.New().ParseRequest([]byte(
		`{"model":"a-very-long-model-identifier-indeed","user":"a-long-user-identifier","messages":[]}`))

	if withPrompt.PromptChars < 10 {
		t.Errorf("prompt chars = %d, want at least the 10 content characters", withPrompt.PromptChars)
	}
	if withoutPrompt.PromptChars != 0 {
		t.Errorf("prompt chars = %d for a request with no prompt text, want 0", withoutPrompt.PromptChars)
	}
}

// TestPromptCharsExcludesStructuralFields was added after a live run showed a
// two-character prompt counted as six: "role":"user" was being counted as
// prompt text along with the content.
//
// The error is small and always upward, but it makes short prompts look
// several times larger than they are.
func TestPromptCharsExcludesStructuralFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want int64
	}{
		{
			name: "role is not prompt text",
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			want: 2,
		},
		{
			name: "several turns count only their content",
			body: `{"model":"gpt-4o","messages":[` +
				`{"role":"user","content":"abcde"},` +
				`{"role":"assistant","content":"fghij"},` +
				`{"role":"user","content":"klmno"}]}`,
			want: 15,
		},
		{
			name: "content part types and urls are not counted",
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":[` +
				`{"type":"text","text":"12345"},` +
				`{"type":"image_url","image_url":{"url":"https://example.invalid/a/very/long/path.png"}}]}]}`,
			want: 5,
		},
		{
			name: "anthropic cache_control is not counted",
			body: `{"model":"claude-sonnet-5","messages":[{"role":"user","content":[` +
				`{"type":"text","text":"1234567890","cache_control":{"type":"ephemeral"}}]}]}`,
			want: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := openai.New().ParseRequest([]byte(tt.body)).PromptChars; got != tt.want {
				t.Errorf("prompt chars = %d, want exactly %d", got, tt.want)
			}
		})
	}
}

func TestUsageFromResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider providers.Provider
		body     string
		wantIn   int64
		wantOut  int64
		wantOK   bool
	}{
		{
			name: "openai", provider: openai.New(),
			body:   `{"usage":{"prompt_tokens":1200,"completion_tokens":800,"total_tokens":2000}}`,
			wantIn: 1200, wantOut: 800, wantOK: true,
		},
		{
			name: "openai responses api naming", provider: openai.New(),
			body:   `{"usage":{"input_tokens":10,"output_tokens":20}}`,
			wantIn: 10, wantOut: 20, wantOK: true,
		},
		{
			name: "anthropic", provider: anthropic.New(),
			body:   `{"usage":{"input_tokens":500,"output_tokens":250}}`,
			wantIn: 500, wantOut: 250, wantOK: true,
		},
		{
			name: "no usage object", provider: openai.New(),
			body: `{"choices":[]}`, wantOK: false,
		},
		{
			name: "usage present but empty", provider: openai.New(),
			body: `{"usage":{}}`, wantOK: false,
		},
		{
			name: "not json", provider: openai.New(),
			body: `not json`, wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tt.provider.UsageFromResponse([]byte(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.InputTokens != tt.wantIn || got.OutputTokens != tt.wantOut {
				t.Errorf("usage = %d/%d, want %d/%d", got.InputTokens, got.OutputTokens, tt.wantIn, tt.wantOut)
			}
		})
	}
}

func TestOpenAICompatibleCacheUsage(t *testing.T) {
	t.Parallel()

	p := openai.New()
	tests := []struct {
		name   string
		body   string
		plain  int64
		cached int64
		write  int64
	}{
		{
			name:  "OpenAI and xAI details",
			body:  `{"usage":{"prompt_tokens":125,"completion_tokens":48,"prompt_tokens_details":{"cached_tokens":98}}}`,
			plain: 27, cached: 98,
		},
		{
			name:  "DeepSeek hit and miss",
			body:  `{"usage":{"prompt_tokens":125,"completion_tokens":48,"prompt_cache_hit_tokens":100,"prompt_cache_miss_tokens":25}}`,
			plain: 25, cached: 100,
		},
		{
			name:  "Kimi top-level cached tokens",
			body:  `{"usage":{"prompt_tokens":125,"completion_tokens":48,"cached_tokens":50}}`,
			plain: 75, cached: 50,
		},
		{
			name:  "OpenAI explicit cache write",
			body:  `{"usage":{"input_tokens":125,"output_tokens":48,"input_tokens_details":{"cached_tokens":50,"cache_write_tokens":25}}}`,
			plain: 50, cached: 50, write: 25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, ok := p.UsageFromResponse([]byte(tt.body))
			if !ok {
				t.Fatal("usage was not detected")
			}
			if u.InputTokens != tt.plain || u.CachedInputTokens != tt.cached || u.CacheWrite5mTokens != tt.write {
				t.Errorf("usage = plain %d cached %d write %d, want %d/%d/%d",
					u.InputTokens, u.CachedInputTokens, u.CacheWrite5mTokens, tt.plain, tt.cached, tt.write)
			}
			if got := u.TotalInputTokens(); got != 125 {
				t.Errorf("total input = %d, want 125", got)
			}
		})
	}
}

func TestAnthropicCacheUsage(t *testing.T) {
	t.Parallel()

	body := `{"usage":{"input_tokens":50,"cache_read_input_tokens":100,"cache_creation_input_tokens":75,"cache_creation":{"ephemeral_5m_input_tokens":25,"ephemeral_1h_input_tokens":50},"output_tokens":20}}`
	u, ok := anthropic.New().UsageFromResponse([]byte(body))
	if !ok {
		t.Fatal("usage was not detected")
	}
	if u.InputTokens != 50 || u.CachedInputTokens != 100 ||
		u.CacheWrite5mTokens != 25 || u.CacheWrite1hTokens != 50 || u.OutputTokens != 20 {
		t.Errorf("unexpected usage: %+v", u)
	}
	if got := u.TotalInputTokens(); got != 225 {
		t.Errorf("total input = %d, want 225", got)
	}
}

// TestAnthropicStreamUsageIsAssembledFromEvents covers the two-part report
// that makes exact pricing possible for a streamed Anthropic call.
func TestAnthropicStreamUsageIsAssembledFromEvents(t *testing.T) {
	t.Parallel()

	p := anthropic.New()
	events := []string{
		`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":500,"output_tokens":1}}}`,
		`{"type":"content_block_delta","delta":{"text":"hello"}}`,
		`{"type":"message_delta","usage":{"output_tokens":120}}`,
		`{"type":"message_delta","usage":{"output_tokens":250}}`,
		`{"type":"message_stop"}`,
	}

	var total providers.Usage
	for _, e := range events {
		if u, ok := p.UsageFromStreamEvent([]byte(e)); ok {
			total.Merge(u)
		}
	}

	if total.InputTokens != 500 {
		t.Errorf("input = %d, want 500 from message_start", total.InputTokens)
	}
	if total.OutputTokens != 250 {
		t.Errorf("output = %d, want 250, the last running count", total.OutputTokens)
	}
}

func TestUsageMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		start, incoming providers.Usage
		wantIn, wantOut int64
	}{
		{
			name: "fills empties", start: providers.Usage{},
			incoming: providers.Usage{InputTokens: 10, OutputTokens: 20}, wantIn: 10, wantOut: 20,
		},
		{
			name:     "a later output count wins",
			start:    providers.Usage{InputTokens: 500, OutputTokens: 10},
			incoming: providers.Usage{OutputTokens: 250}, wantIn: 500, wantOut: 250,
		},
		{
			name:     "zero does not clobber",
			start:    providers.Usage{InputTokens: 500, OutputTokens: 250},
			incoming: providers.Usage{}, wantIn: 500, wantOut: 250,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.start
			got.Merge(tt.incoming)
			if got.InputTokens != tt.wantIn || got.OutputTokens != tt.wantOut {
				t.Errorf("merged = %d/%d, want %d/%d", got.InputTokens, got.OutputTokens, tt.wantIn, tt.wantOut)
			}
		})
	}

	if !(providers.Usage{}).IsZero() {
		t.Error("an empty usage does not report zero")
	}
	if (providers.Usage{OutputTokens: 1}).IsZero() {
		t.Error("a usage with output reports zero")
	}
}
