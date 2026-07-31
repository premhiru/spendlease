// Package openai adapts OpenAI and OpenAI-compatible endpoints.
package openai

import (
	"encoding/json"
	"fmt"
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
	name    string
	paths   []string
}

var defaultPaths = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/responses",
	"/v1/embeddings",
	"/v1/moderations",
	"/v1/images",
	"/v1/audio",
	"/v1/models",
}

// New returns an adapter pointed at the default OpenAI API.
func New() *Provider {
	u, _ := url.Parse(DefaultBaseURL)
	return &Provider{baseURL: u, name: Name, paths: defaultPaths}
}

// NewWithBaseURL returns an adapter pointed somewhere else, which is how the
// tests aim it at a fake upstream and how an operator points it at an
// OpenAI-compatible gateway.
func NewWithBaseURL(raw string) (*Provider, error) {
	return NewCompatible(Name, raw)
}

// NewCompatible creates a named adapter for an OpenAI-compatible vendor.
// The explicit /<provider>/ prefix is how callers select it when several
// vendors expose the same paths.
func NewCompatible(name, raw string) (*Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("provider name must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &Provider{baseURL: u, name: name, paths: defaultPaths}, nil
}

// Name returns the provider identifier used to construct the adapter.
func (p *Provider) Name() string { return p.name }

// BaseURL returns the upstream root.
func (p *Provider) BaseURL() *url.URL { return p.baseURL }

// Paths returns the request prefixes OpenAI claims.
//
// /v1/models is also claimed by Anthropic; the registry disambiguates. Every
// other path here is OpenAI-only.
func (p *Provider) Paths() []string {
	return p.paths
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
	return providers.OpenAIUsageFrom(m)
}

// UsageFromStreamEvent reads usage from one streamed chunk.
//
// OpenAI sends usage only in a final chunk, and only when
// stream_options.include_usage was set — by the caller, or by the gateway on
// their behalf.
func (p *Provider) UsageFromStreamEvent(data []byte) (providers.Usage, bool) {
	return p.UsageFromResponse(data)
}

// EnableStreamUsage sets stream_options.include_usage on a streaming request.
//
// Without it OpenAI reports no usage for a streamed call and the cost can only
// be estimated. The change is confined to that one field: the body is decoded,
// the field is set, and everything else is re-encoded as it was.
//
// It returns false, and the body untouched, when the request is not streaming
// or already asks for usage.
func (p *Provider) EnableStreamUsage(body []byte) ([]byte, bool) {
	m := providers.DecodeBody(body)
	if m == nil || !providers.BoolField(m, "stream") {
		return body, false
	}

	opts, _ := m["stream_options"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	} else if include, ok := opts["include_usage"].(bool); ok && include {
		return body, false
	}

	opts["include_usage"] = true
	m["stream_options"] = opts

	modified, err := json.Marshal(m)
	if err != nil {
		// Leave the request exactly as it arrived. Losing exact accounting is
		// a far smaller failure than corrupting somebody's request.
		return body, false
	}
	return modified, true
}

// IsUsageOnlyEvent reports whether a chunk carries usage and no content.
//
// The chunk that include_usage produces has an empty choices array, which is
// how it is told apart from an ordinary content chunk that happens to arrive
// alongside usage.
func (p *Provider) IsUsageOnlyEvent(data []byte) bool {
	m := providers.DecodeBody(data)
	if m == nil {
		return false
	}
	if _, hasUsage := m["usage"].(map[string]any); !hasUsage {
		return false
	}
	choices, ok := m["choices"].([]any)
	return !ok || len(choices) == 0
}
