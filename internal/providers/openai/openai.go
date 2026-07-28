// Package openai adapts OpenAI and OpenAI-compatible endpoints.
package openai

import (
	"net/http"
	"net/url"
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
