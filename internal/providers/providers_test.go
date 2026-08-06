package providers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
)

func newRegistry(t *testing.T) *providers.Registry {
	t.Helper()

	r, err := providers.NewRegistry(openai.New(), anthropic.New())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		path         string
		headers      map[string]string
		wantProvider string
		wantPath     string
		wantErr      bool
	}{
		// Unambiguous OpenAI routes.
		{name: "chat completions", path: "/v1/chat/completions", wantProvider: "openai", wantPath: "/v1/chat/completions"},
		{name: "legacy completions", path: "/v1/completions", wantProvider: "openai", wantPath: "/v1/completions"},
		{name: "responses", path: "/v1/responses", wantProvider: "openai", wantPath: "/v1/responses"},
		{name: "embeddings", path: "/v1/embeddings", wantProvider: "openai", wantPath: "/v1/embeddings"},
		{name: "nested audio route", path: "/v1/audio/speech", wantProvider: "openai", wantPath: "/v1/audio/speech"},

		// Unambiguous Anthropic routes.
		{name: "messages", path: "/v1/messages", wantProvider: "anthropic", wantPath: "/v1/messages"},
		{name: "message batches", path: "/v1/messages/batches", wantProvider: "anthropic", wantPath: "/v1/messages/batches"},
		{name: "legacy complete", path: "/v1/complete", wantProvider: "anthropic", wantPath: "/v1/complete"},

		// Ambiguous: both providers claim /v1/models.
		{
			name: "ambiguous path falls back to the first provider",
			path: "/v1/models", wantProvider: "openai", wantPath: "/v1/models",
		},
		{
			name: "the anthropic-version header disambiguates",
			path: "/v1/models", headers: map[string]string{"anthropic-version": "2023-06-01"},
			wantProvider: "anthropic", wantPath: "/v1/models",
		},

		// Explicit prefixes always win and are stripped.
		{name: "explicit openai prefix", path: "/openai/v1/messages", wantProvider: "openai", wantPath: "/v1/messages"},
		{name: "explicit anthropic prefix", path: "/anthropic/v1/chat/completions", wantProvider: "anthropic", wantPath: "/v1/chat/completions"},
		{
			name: "explicit prefix beats the disambiguating header",
			path: "/openai/v1/models", headers: map[string]string{"anthropic-version": "2023-06-01"},
			wantProvider: "openai", wantPath: "/v1/models",
		},
		{
			name:         "an explicit prefix reaches a path no provider claims",
			path:         "/openai/v1/some/future/endpoint",
			wantProvider: "openai", wantPath: "/v1/some/future/endpoint",
		},

		// Failures.
		{name: "unknown path", path: "/v2/nonsense", wantErr: true},
		{name: "root", path: "/", wantErr: true},
		{name: "unknown provider prefix", path: "/cohere/v1/chat", wantErr: true},
		{
			name: "a prefix of a claimed path is not a match",
			path: "/v1/messagesextra", wantErr: true,
		},
	}

	registry := newRegistry(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			p, path, err := registry.Resolve(req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%s) = %s, want an error", tt.path, p.Name())
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tt.path, err)
			}
			if p.Name() != tt.wantProvider {
				t.Errorf("provider = %q, want %q", p.Name(), tt.wantProvider)
			}
			if path != tt.wantPath {
				t.Errorf("upstream path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestRegistryConstruction(t *testing.T) {
	t.Parallel()

	t.Run("needs at least one provider", func(t *testing.T) {
		t.Parallel()
		if _, err := providers.NewRegistry(); err == nil {
			t.Error("an empty registry was accepted")
		}
	})

	t.Run("rejects duplicates", func(t *testing.T) {
		t.Parallel()
		if _, err := providers.NewRegistry(openai.New(), openai.New()); err == nil {
			t.Error("a duplicate provider was accepted")
		}
	})

	t.Run("names are sorted", func(t *testing.T) {
		t.Parallel()
		got := newRegistry(t).Names()
		want := []string{"anthropic", "openai"}
		if len(got) != len(want) {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Names() = %v, want %v", got, want)
			}
		}
	})

	t.Run("lookup by name", func(t *testing.T) {
		t.Parallel()
		r := newRegistry(t)
		if p, ok := r.Lookup("openai"); !ok || p.Name() != "openai" {
			t.Error("Lookup(openai) failed")
		}
		if _, ok := r.Lookup("cohere"); ok {
			t.Error("Lookup returned an unregistered provider")
		}
	})
}

func TestAuthorize(t *testing.T) {
	t.Parallel()

	t.Run("openai sets a bearer token", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		openai.New().Authorize(req, "sk-vendor")

		if got := req.Header.Get("Authorization"); got != "Bearer sk-vendor" {
			t.Errorf("Authorization = %q", got)
		}
	})

	t.Run("anthropic sets x-api-key and clears any bearer", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer leftover")
		anthropic.New().Authorize(req, "sk-ant-vendor")

		if got := req.Header.Get("x-api-key"); got != "sk-ant-vendor" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := req.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want it cleared", got)
		}
		if got := req.Header.Get("anthropic-version"); got != anthropic.DefaultVersion {
			t.Errorf("anthropic-version = %q, want the default", got)
		}
	})

	t.Run("anthropic keeps a version the caller chose", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("anthropic-version", "2099-01-01")
		anthropic.New().Authorize(req, "sk-ant-vendor")

		if got := req.Header.Get("anthropic-version"); got != "2099-01-01" {
			t.Errorf("anthropic-version = %q, want the caller's value preserved", got)
		}
	})
}

func TestBaseURLOverride(t *testing.T) {
	t.Parallel()

	oa, err := openai.NewWithBaseURL("http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("NewWithBaseURL: %v", err)
	}
	if got := oa.BaseURL().Host; got != "127.0.0.1:1234" {
		t.Errorf("BaseURL host = %q", got)
	}

	if _, err := openai.NewWithBaseURL("://bad"); err == nil {
		t.Error("an invalid base URL was accepted")
	}
}

func TestBillingCapabilities(t *testing.T) {
	t.Parallel()

	oa := openai.New()
	if got := oa.Billing(http.MethodPost, "/v1/chat/completions"); got.Class != providers.BillingToken || got.NoOutput {
		t.Fatalf("chat billing = %+v", got)
	}
	if got := oa.Billing(http.MethodPost, "/v1/embeddings"); got.Class != providers.BillingToken || !got.NoOutput {
		t.Fatalf("embedding billing = %+v", got)
	}
	if got := oa.Billing(http.MethodPost, "/v1/images/generations"); got.Class != providers.BillingUnsupported {
		t.Fatalf("image billing = %+v", got)
	}
	if got := anthropic.New().Billing(http.MethodPost, "/v1/messages/batches"); got.Class != providers.BillingUnsupported {
		t.Fatalf("batch billing = %+v", got)
	}
}

func TestRequestBillingGuards(t *testing.T) {
	t.Parallel()

	info := openai.New().ParseRequest([]byte(`{"model":"gpt-4o","max_output_tokens":37,"input":"hello"}`))
	if info.MaxTokens != 37 || info.RequestBytes == 0 {
		t.Fatalf("responses request = %+v", info)
	}
	for _, body := range []string{
		`{"model":"gpt-4o","input_image":{"url":"x"}}`,
		`{"model":"gpt-4o","input":[{"type":"input_file","file_id":"file-1"}]}`,
		`{"model":"claude-sonnet-4-5","messages":[{"content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`,
		`{"model":"gpt-4o","tools":[{"type":"web_search_preview"}]}`,
	} {
		if got := openai.New().ParseRequest([]byte(body)).UnsupportedBilling; got == "" {
			t.Fatalf("request %s was not marked unsupported", body)
		}
	}
}

func TestRequestPricingModifierGuards(t *testing.T) {
	t.Parallel()

	gemini, err := openai.NewCompatible("gemini", "https://generativelanguage.googleapis.com")
	if err != nil {
		t.Fatalf("NewCompatible: %v", err)
	}
	tests := []struct {
		name        string
		parse       func([]byte) providers.RequestInfo
		body        string
		unsupported bool
	}{
		{name: "OpenAI omitted tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o"}`},
		{name: "OpenAI null tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o","service_tier":null}`},
		{name: "OpenAI default tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o","service_tier":"default"}`},
		{name: "OpenAI auto tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o","service_tier":"auto"}`, unsupported: true},
		{name: "OpenAI priority tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o","service_tier":"priority"}`, unsupported: true},
		{name: "OpenAI flex tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o","service_tier":"flex"}`, unsupported: true},
		{name: "OpenAI unknown tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o","service_tier":"future"}`, unsupported: true},
		{name: "OpenAI non-string tier", parse: openai.New().ParseRequest, body: `{"model":"gpt-4o","service_tier":1}`, unsupported: true},
		{name: "Gemini standard tier", parse: gemini.ParseRequest, body: `{"model":"gemini-3.1-pro-preview","service_tier":"standard"}`},
		{name: "Gemini priority tier", parse: gemini.ParseRequest, body: `{"model":"gemini-3.1-pro-preview","service_tier":"priority"}`, unsupported: true},
		{name: "Anthropic omitted modifiers", parse: anthropic.New().ParseRequest, body: `{"model":"claude-sonnet-5"}`},
		{name: "Anthropic ordinary modifiers", parse: anthropic.New().ParseRequest, body: `{"model":"claude-sonnet-5","service_tier":"standard_only","speed":"standard","inference_geo":"global"}`},
		{name: "Anthropic auto tier", parse: anthropic.New().ParseRequest, body: `{"model":"claude-sonnet-5","service_tier":"auto"}`, unsupported: true},
		{name: "Anthropic fast mode", parse: anthropic.New().ParseRequest, body: `{"model":"claude-sonnet-5","speed":"fast"}`, unsupported: true},
		{name: "Anthropic US inference", parse: anthropic.New().ParseRequest, body: `{"model":"claude-sonnet-5","inference_geo":"us"}`, unsupported: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.parse([]byte(tt.body)).UnsupportedBilling
			if tt.unsupported && got == "" {
				t.Fatal("request was not marked unsupported")
			}
			if !tt.unsupported && got != "" {
				t.Fatalf("ordinary request was marked unsupported: %s", got)
			}
		})
	}
}

func TestOpenAICompatibleProviderUsesExplicitPrefix(t *testing.T) {
	t.Parallel()

	oa := openai.New()
	deepseek, err := openai.NewCompatible("deepseek", "https://api.deepseek.com")
	if err != nil {
		t.Fatalf("NewCompatible: %v", err)
	}
	r, err := providers.NewRegistry(oa, deepseek)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/deepseek/v1/chat/completions", nil)
	p, path, err := r.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Name() != "deepseek" || path != "/v1/chat/completions" {
		t.Errorf("resolved to %s %s, want deepseek /v1/chat/completions", p.Name(), path)
	}
}

func TestDefaultBaseURLs(t *testing.T) {
	t.Parallel()

	if got := openai.New().BaseURL().String(); got != openai.DefaultBaseURL {
		t.Errorf("openai base = %q, want %q", got, openai.DefaultBaseURL)
	}
	if got := anthropic.New().BaseURL().String(); got != anthropic.DefaultBaseURL {
		t.Errorf("anthropic base = %q, want %q", got, anthropic.DefaultBaseURL)
	}
}
