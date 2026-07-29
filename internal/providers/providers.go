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

	// Authorize attaches the vendor credential to an outbound request. It is
	// called after the client's own Authorization header has been stripped,
	// so an implementation should set rather than append.
	Authorize(req *http.Request, apiKey string)
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
