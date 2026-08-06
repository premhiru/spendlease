//go:build live

package providers_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/openai"
)

type liveProvider struct {
	name         string
	defaultURL   string
	defaultModel string
	keyEnv       string
	urlEnv       string
	modelEnv     string
}

func TestLiveOpenAICompatibleProviders(t *testing.T) {
	configs := []liveProvider{
		{name: "kimi", defaultURL: "https://api.moonshot.ai/v1/chat/completions", defaultModel: "kimi-k2.6", keyEnv: "SPENDLEASE_SMOKE_KIMI_KEY", urlEnv: "SPENDLEASE_SMOKE_KIMI_URL", modelEnv: "SPENDLEASE_SMOKE_KIMI_MODEL"},
		{name: "deepseek", defaultURL: "https://api.deepseek.com/v1/chat/completions", defaultModel: "deepseek-v4-flash", keyEnv: "SPENDLEASE_SMOKE_DEEPSEEK_KEY", urlEnv: "SPENDLEASE_SMOKE_DEEPSEEK_URL", modelEnv: "SPENDLEASE_SMOKE_DEEPSEEK_MODEL"},
		{name: "xai", defaultURL: "https://api.x.ai/v1/chat/completions", defaultModel: "grok-4.3", keyEnv: "SPENDLEASE_SMOKE_XAI_KEY", urlEnv: "SPENDLEASE_SMOKE_XAI_URL", modelEnv: "SPENDLEASE_SMOKE_XAI_MODEL"},
		{name: "gemini", defaultURL: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", defaultModel: "gemini-3.1-flash-lite", keyEnv: "SPENDLEASE_SMOKE_GEMINI_KEY", urlEnv: "SPENDLEASE_SMOKE_GEMINI_URL", modelEnv: "SPENDLEASE_SMOKE_GEMINI_MODEL"},
		{name: "zai", defaultURL: "https://api.z.ai/api/paas/v4/chat/completions", defaultModel: "glm-4.7-flash", keyEnv: "SPENDLEASE_SMOKE_ZAI_KEY", urlEnv: "SPENDLEASE_SMOKE_ZAI_URL", modelEnv: "SPENDLEASE_SMOKE_ZAI_MODEL"},
	}

	configured := make([]liveProvider, 0, len(configs))
	for _, cfg := range configs {
		if strings.TrimSpace(os.Getenv(cfg.keyEnv)) != "" {
			configured = append(configured, cfg)
		}
	}
	if len(configured) == 0 {
		t.Skip("no live provider credentials are configured")
	}

	for _, cfg := range configured {
		cfg := cfg
		t.Run(cfg.name, func(t *testing.T) {
			adapter, err := openai.NewCompatible(cfg.name, "https://example.test")
			if err != nil {
				t.Fatalf("adapter: %v", err)
			}
			for _, stream := range []bool{false, true} {
				stream := stream
				t.Run(map[bool]string{false: "non_stream", true: "stream"}[stream], func(t *testing.T) {
					usage := callLiveProvider(t, adapter, cfg, stream)
					if usage.TotalInputTokens() == 0 && usage.OutputTokens == 0 {
						t.Fatal("provider returned no billable usage")
					}
				})
			}
		})
	}
}

func callLiveProvider(t *testing.T, adapter *openai.Provider, cfg liveProvider, stream bool) providers.Usage {
	t.Helper()

	endpoint := envOr(cfg.urlEnv, cfg.defaultURL)
	model := envOr(cfg.modelEnv, cfg.defaultModel)
	payload := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Reply with the single digit 2."}},
		"max_tokens": 1,
		"stream":     stream,
	}
	if stream {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(os.Getenv(cfg.keyEnv)))
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		t.Fatalf("provider returned %s: %s", res.Status, strings.TrimSpace(string(detail)))
	}

	if !stream {
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		usage, ok := adapter.UsageFromResponse(raw)
		if !ok {
			t.Fatalf("response did not contain supported usage: %s", bounded(raw, 2048))
		}
		return usage
	}

	var usage providers.Usage
	found := false
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:")))
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if eventUsage, ok := adapter.UsageFromStreamEvent(data); ok {
			usage.Merge(eventUsage)
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !found {
		t.Fatal("stream ended without a supported usage event")
	}
	return usage
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func bounded(raw []byte, limit int) string {
	if len(raw) <= limit {
		return string(raw)
	}
	return fmt.Sprintf("%s... (%d bytes total)", raw[:limit], len(raw))
}
