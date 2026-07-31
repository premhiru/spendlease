// Package anthropic adapts the Anthropic Messages API.
package anthropic

import (
	"net/http"
	"net/url"

	"github.com/premhiru/spendlease/internal/providers"
)

// Name is the provider identifier used in leases, the price book and the
// ledger.
const Name = "anthropic"

// DefaultBaseURL is the upstream Anthropic API.
const DefaultBaseURL = "https://api.anthropic.com"

// DefaultVersion is sent as anthropic-version when a client does not set it.
// The Anthropic API rejects requests without this header, and an SDK pointed
// at a base URL always sets it — but a bare curl might not, and failing with
// a vendor error the user cannot act on is a poor experience.
const DefaultVersion = "2023-06-01"

// Provider is the Anthropic adapter.
type Provider struct {
	baseURL *url.URL
}

// New returns an adapter pointed at the default Anthropic API.
func New() *Provider {
	u, _ := url.Parse(DefaultBaseURL)
	return &Provider{baseURL: u}
}

// NewWithBaseURL returns an adapter pointed somewhere else, used by tests to
// aim at a fake upstream.
func NewWithBaseURL(raw string) (*Provider, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &Provider{baseURL: u}, nil
}

// Name returns "anthropic".
func (p *Provider) Name() string { return Name }

// BaseURL returns the upstream root.
func (p *Provider) BaseURL() *url.URL { return p.baseURL }

// Paths returns the request prefixes Anthropic claims.
//
// /v1/models is also claimed by OpenAI; the registry disambiguates using the
// anthropic-version header.
func (p *Provider) Paths() []string {
	return []string{
		"/v1/messages",
		"/v1/complete",
		"/v1/models",
	}
}

// Authorize sets the Anthropic credential and ensures an API version is
// present.
//
// Anthropic authenticates with x-api-key rather than a bearer token. The
// Authorization header is explicitly cleared because the gateway strips the
// client's own credential and nothing should reintroduce one.
func (p *Provider) Authorize(req *http.Request, apiKey string) {
	req.Header.Del("Authorization")
	req.Header.Set("x-api-key", apiKey)
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", DefaultVersion)
	}
}

// ParseRequest reads the model, output ceiling, streaming flag and prompt
// size from a Messages API request body.
//
// WantsUsage is always true: unlike OpenAI, Anthropic reports usage on every
// streamed response without being asked, so spendlease can always record
// exact token counts for this vendor.
func (p *Provider) ParseRequest(body []byte) providers.RequestInfo {
	m := providers.DecodeBody(body)
	if m == nil {
		return providers.RequestInfo{}
	}
	return providers.RequestInfo{
		Model:       providers.StringField(m, "model"),
		MaxTokens:   providers.IntField(m, "max_tokens"),
		Stream:      providers.BoolField(m, "stream"),
		PromptChars: providers.CountPromptChars(m),
		WantsUsage:  true,
	}
}

// UsageFromResponse reads Anthropic's usage object from a complete response.
func (p *Provider) UsageFromResponse(body []byte) (providers.Usage, bool) {
	m := providers.DecodeBody(body)
	if m == nil {
		return providers.Usage{}, false
	}
	return usageFrom(m)
}

// UsageFromStreamEvent reads usage from one streamed event.
//
// Anthropic reports usage in two places: message_start carries the input
// count nested under "message", and message_delta carries a running output
// count. Merging both is what produces an exact total, so no estimate is
// needed for a streamed Anthropic call.
func (p *Provider) UsageFromStreamEvent(data []byte) (providers.Usage, bool) {
	return p.UsageFromResponse(data)
}

// EnableStreamUsage does nothing for Anthropic.
//
// The Messages API reports usage on every streamed response without being
// asked, so there is nothing to enable — and mutating a request that does not
// need it would be gratuitous.
func (p *Provider) EnableStreamUsage(body []byte) ([]byte, bool) { return body, false }

// IsUsageOnlyEvent is always false for Anthropic.
//
// Its usage arrives on message_start and message_delta, which are part of the
// normal event sequence a client expects. Withholding them would break the
// stream rather than tidy it.
func (p *Provider) IsUsageOnlyEvent([]byte) bool { return false }

func usageFrom(m map[string]any) (providers.Usage, bool) {
	raw, ok := m["usage"].(map[string]any)
	if !ok {
		if nested, ok := m["message"].(map[string]any); ok {
			return usageFrom(nested)
		}
		return providers.Usage{}, false
	}

	u := providers.Usage{
		InputTokens:       providers.IntField(raw, "input_tokens"),
		CachedInputTokens: providers.IntField(raw, "cache_read_input_tokens"),
		OutputTokens:      providers.IntField(raw, "output_tokens"),
	}
	if creation, ok := raw["cache_creation"].(map[string]any); ok {
		u.CacheWrite5mTokens = providers.IntField(creation, "ephemeral_5m_input_tokens")
		u.CacheWrite1hTokens = providers.IntField(creation, "ephemeral_1h_input_tokens")
	}
	// Older responses report only the aggregate. Anthropic's default cache
	// lifetime is five minutes, so any unexplained creation tokens belong in
	// that bucket.
	totalCreation := providers.IntField(raw, "cache_creation_input_tokens")
	if remainder := totalCreation - u.CacheWrite5mTokens - u.CacheWrite1hTokens; remainder > 0 {
		u.CacheWrite5mTokens += remainder
	}
	return u, !u.IsZero()
}
