// Package openai adapts OpenAI and OpenAI-compatible endpoints.
package openai

import (
	"net/http"
	"net/url"

	"github.com/premhiru/spendlease/internal/providers"
)

// Name is the provider identifier used in leases, the price book and the
// ledger.
const Name = "openai"

// DefaultBaseURL is the upstream OpenAI API.
const DefaultBaseURL = "https://api.openai.com"

// Provider is the OpenAI adapter.
type Provider struct {
	baseURL *url.URL
}

// New returns an adapter pointed at the default OpenAI API.
func New() *Provider {
	u, _ := url.Parse(DefaultBaseURL)
	return &Provider{baseURL: u}
}

// NewWithBaseURL returns an adapter pointed somewhere else, which is how the
// tests aim it at a fake upstream and how an operator points it at an
// OpenAI-compatible gateway.
func NewWithBaseURL(raw string) (*Provider, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &Provider{baseURL: u}, nil
}

// Name returns "openai".
func (p *Provider) Name() string { return Name }

// BaseURL returns the upstream root.
func (p *Provider) BaseURL() *url.URL { return p.baseURL }

// Paths returns the request prefixes OpenAI claims.
//
// /v1/models is also claimed by Anthropic; the registry disambiguates. Every
// other path here is OpenAI-only.
func (p *Provider) Paths() []string {
	return []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/responses",
		"/v1/embeddings",
		"/v1/moderations",
		"/v1/images",
		"/v1/audio",
		"/v1/models",
	}
}

// Authorize sets the OpenAI bearer credential.
func (p *Provider) Authorize(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

// ParseRequest reads the model, output ceiling, streaming flag and prompt
// size from an OpenAI-shaped request body.
//
// Both max_tokens and its replacement max_completion_tokens are read, because
// the newer models accept only the latter and older clients still send the
// former.
func (p *Provider) ParseRequest(body []byte) providers.RequestInfo {
	m := providers.DecodeBody(body)
	if m == nil {
		return providers.RequestInfo{}
	}

	info := providers.RequestInfo{
		Model:       providers.StringField(m, "model"),
		MaxTokens:   providers.IntField(m, "max_tokens", "max_completion_tokens"),
		Stream:      providers.BoolField(m, "stream"),
		PromptChars: providers.CountPromptChars(m),
	}

	// Usage on a streamed OpenAI response is opt-in. Whether the caller asked
	// decides whether spendlease can record exact usage or has to estimate.
	if opts, ok := m["stream_options"].(map[string]any); ok {
		info.WantsUsage = providers.BoolField(opts, "include_usage")
	}
	return info
}

// UsageFromResponse reads OpenAI's usage object from a complete response.
func (p *Provider) UsageFromResponse(body []byte) (providers.Usage, bool) {
	m := providers.DecodeBody(body)
	if m == nil {
		return providers.Usage{}, false
	}
	return providers.UsageFrom(m,
		[]string{"prompt_tokens", "input_tokens"},
		[]string{"completion_tokens", "output_tokens"})
}

// UsageFromStreamEvent reads usage from one streamed chunk.
//
// OpenAI sends usage only in a final chunk, and only when the caller set
// stream_options.include_usage. Without it there is nothing to read and the
// gateway falls back to its local estimate.
func (p *Provider) UsageFromStreamEvent(data []byte) (providers.Usage, bool) {
	return p.UsageFromResponse(data)
}
