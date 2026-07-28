// Package anthropic adapts the Anthropic Messages API.
package anthropic

import (
	"net/http"
	"net/url"
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
