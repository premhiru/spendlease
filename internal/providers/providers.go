// Package providers holds the per-vendor adapters and the routing that picks
// one for an incoming request.
//
// An adapter knows three things: where the vendor lives, which request paths
// belong to it, and how to attach the vendor's credential. Everything else —
// proxying, streaming, accounting — is the gateway's job and is identical
// across vendors.
package providers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ErrUnknownRoute means no adapter claims the request path.
var ErrUnknownRoute = errors.New("providers: no provider handles this path")

// Provider is one vendor adapter.
type Provider interface {
	// Name is the identifier used in leases, the price book and the ledger,
	// for example "openai".
	Name() string

	// BaseURL is where requests are forwarded.
	BaseURL() *url.URL

	// Paths returns the request path prefixes this provider claims.
	Paths() []string

	// Billing classifies an upstream route before it is contacted. The
	// gateway uses this to avoid presenting best-effort token accounting as a
	// hard limit for media, batch, tool-fee, or otherwise unsupported spend.
	Billing(method, path string) BillingCapability

	// Authorize attaches the vendor credential to an outbound request. It is
	// called after the client's own Authorization header has been stripped,
	// so an implementation should set rather than append.
	Authorize(req *http.Request, apiKey string)

	// ParseRequest reads what the gateway needs from a request body: the
	// model, the output ceiling, whether the response streams, and the
	// prompt size.
	//
	// It never returns an error. A body it cannot understand yields a
	// zero-valued RequestInfo; the gateway forwards that only in observe mode
	// and fails closed when spend enforcement is enabled.
	ParseRequest(body []byte) RequestInfo

	// UsageFromResponse reads the token counts a vendor reports on a
	// complete, non-streaming response. The bool is false when the response
	// carried no usage.
	UsageFromResponse(body []byte) (Usage, bool)

	// UsageFromStreamEvent reads the token counts carried by one server-sent
	// event payload, which vendors report in pieces across a stream. The
	// bool is false for the majority of events, which carry only content.
	UsageFromStreamEvent(data []byte) (Usage, bool)

	// EnableStreamUsage returns a request body modified to make the vendor
	// report usage on a streamed response, and whether anything changed.
	//
	// This exists because OpenAI reports usage on a stream only when asked.
	// Without it a streamed call cannot be priced exactly. It mutates the
	// caller's request, which is surprising, so a provider that already
	// reports usage must return the body unchanged and false.
	EnableStreamUsage(body []byte) ([]byte, bool)

	// IsUsageOnlyEvent reports whether a streamed event carries usage and no
	// content — the extra chunk that EnableStreamUsage causes.
	//
	// When the gateway asked for usage on the caller's behalf, that chunk is
	// withheld so the caller's stream looks exactly as it would have without
	// spendlease in the path.
	IsUsageOnlyEvent(data []byte) bool
}

// BillingClass describes whether spendlease can authorize a route's cost.
type BillingClass string

const (
	// BillingToken means the route is priced from input and output tokens.
	BillingToken BillingClass = "token"
	// BillingNoSpend means the route is operational and is not expected to
	// incur provider spend, such as listing models.
	BillingNoSpend BillingClass = "no_spend"
	// BillingUnsupported means the route may incur charges that the active
	// token price book cannot bound.
	BillingUnsupported BillingClass = "unsupported"
)

// BillingCapability is one route's accounting contract.
type BillingCapability struct {
	Class BillingClass
	// NoOutput is true for token-billed routes such as embeddings that have
	// input usage but no generated output to reserve.
	NoOutput bool
	// Reason is shown when enforcement refuses unsupported spend.
	Reason string
}

// Registry resolves an incoming request to a provider.
//
// Routing is by path prefix, which is what makes the one-line base-URL
// integration work: an OpenAI SDK pointed at /v1 sends /v1/chat/completions,
// an Anthropic SDK sends /v1/messages, and those are different paths.
//
// Two escape hatches exist for the cases path alone cannot settle:
//
//   - An explicit /<provider>/... prefix always wins and is stripped before
//     forwarding. Use it when a path is ambiguous or a vendor adds a route
//     this package does not know about.
//   - For paths claimed by more than one provider (/v1/models is the current
//     example), the "anthropic-version" request header disambiguates, because
//     only Anthropic clients send it.
type Registry struct {
	byName map[string]Provider
	order  []Provider
	// fallback receives ambiguous paths when no other signal is available.
	fallback Provider
}

// NewRegistry builds a registry. The first provider given is the fallback for
// ambiguous paths.
func NewRegistry(ps ...Provider) (*Registry, error) {
	if len(ps) == 0 {
		return nil, errors.New("providers: registry needs at least one provider")
	}

	r := &Registry{byName: make(map[string]Provider, len(ps))}
	for _, p := range ps {
		if _, dup := r.byName[p.Name()]; dup {
			return nil, fmt.Errorf("providers: %q registered twice", p.Name())
		}
		r.byName[p.Name()] = p
		r.order = append(r.order, p)
	}
	r.fallback = ps[0]
	return r, nil
}

// Names returns the registered provider names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for name := range r.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Lookup returns a provider by name.
func (r *Registry) Lookup(name string) (Provider, bool) {
	p, ok := r.byName[name]
	return p, ok
}

// Resolve picks the provider for a request and returns the path that should
// be forwarded upstream.
//
// The returned path differs from the request path only when an explicit
// /<provider>/ prefix was used, in which case the prefix is stripped.
func (r *Registry) Resolve(req *http.Request) (Provider, string, error) {
	path := req.URL.Path

	// An explicit /<provider>/... prefix is unambiguous and always wins.
	if name, rest, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/"); ok {
		if p, found := r.byName[name]; found {
			return p, "/" + rest, nil
		}
	}

	matches := r.matching(path)
	switch len(matches) {
	case 0:
		return nil, "", fmt.Errorf("%w: %s", ErrUnknownRoute, path)
	case 1:
		return matches[0], path, nil
	}

	// Ambiguous. Only Anthropic clients send this header.
	if req.Header.Get("anthropic-version") != "" {
		for _, p := range matches {
			if p.Name() == "anthropic" {
				return p, path, nil
			}
		}
	}
	for _, p := range matches {
		if p == r.fallback {
			return p, path, nil
		}
	}
	return matches[0], path, nil
}

// matching returns every provider claiming a prefix of path, in registration
// order.
func (r *Registry) matching(path string) []Provider {
	var out []Provider
	for _, p := range r.order {
		for _, prefix := range p.Paths() {
			if path == prefix || strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/") {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
