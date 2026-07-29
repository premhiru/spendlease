// Package gateway is the reverse proxy that sits between agents and vendor
// APIs.
//
// In this phase it authenticates the caller, resolves the vendor, swaps the
// caller's spendlease credential for the real vendor key, and proxies the
// request — including streaming responses, untouched and unbuffered. There is
// no cost logic yet: nothing is priced, reserved or recorded. That arrives in
// later phases and slots in around this proxying rather than replacing it.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/vault"
)

// PrincipalLookup is the slice of the store the gateway needs to authenticate
// a request. Keeping it narrow makes the dependency obvious and the tests
// cheap.
type PrincipalLookup interface {
	// GetPrincipalByKeyHash resolves a presented credential's hash to its
	// principal.
	GetPrincipalByKeyHash(ctx context.Context, keyHash string) (store.Principal, error)
}

// LeaseLookup resolves lease authentication into its run and principal.
type LeaseLookup interface {
	GetLeaseByTokenHash(ctx context.Context, tokenHash string) (store.Lease, error)
	GetRun(ctx context.Context, id string) (store.Run, error)
	GetPrincipal(ctx context.Context, id string) (store.Principal, error)
}

// CredentialSource supplies vendor API keys at egress.
type CredentialSource interface {
	// Get returns the plaintext vendor key for a provider.
	Get(ctx context.Context, provider string) (string, error)
}

// Options configures a Gateway.
type Options struct {
	// Principals authenticates incoming requests.
	Principals PrincipalLookup
	// Leases authenticates short-lived agent credentials. Optional only for
	// proxy unit tests; production supplies the store.
	Leases LeaseLookup
	// Revocations is checked in memory before a revoked lease reaches storage.
	Revocations *RevocationSet
	// Credentials supplies vendor keys at egress.
	Credentials CredentialSource
	// Registry routes requests to providers.
	Registry *providers.Registry
	// Recorder prices completed requests and appends them to the ledger.
	// Optional: without one the gateway proxies without accounting, which is
	// what the tests that only care about proxying use.
	Recorder *Recorder
	// Dashboard, if set, claims the root path and the admin routes. Without
	// one the root serves a plain-text banner.
	Dashboard RouteRegistrar
	// Logger receives structured request logs. Required.
	Logger *slog.Logger
	// Transport is used for upstream requests. Defaults to
	// http.DefaultTransport; tests substitute their own.
	Transport http.RoundTripper
	// UpstreamTimeout bounds a non-streaming upstream request. Zero means no
	// timeout, which is the right default because a long streaming completion
	// is normal and indistinguishable from a hang at request time.
	UpstreamTimeout time.Duration
}

// RouteRegistrar is anything that adds its own handlers to the gateway's mux.
//
// The dashboard is passed in this shape rather than imported directly so the
// gateway does not depend on rendering, and so proxy tests need no templates.
type RouteRegistrar interface {
	// Routes registers handlers on the mux.
	Routes(mux *http.ServeMux)
}

// Gateway proxies agent requests to vendor APIs.
type Gateway struct {
	principals  PrincipalLookup
	leases      LeaseLookup
	revocations *RevocationSet
	credentials CredentialSource
	registry    *providers.Registry
	recorder    *Recorder
	dashboard   RouteRegistrar
	logger      *slog.Logger
	transport   http.RoundTripper
}

// New returns a gateway.
func New(opts Options) (*Gateway, error) {
	if opts.Principals == nil {
		return nil, errors.New("gateway: Principals is required")
	}
	if opts.Credentials == nil {
		return nil, errors.New("gateway: Credentials is required")
	}
	if opts.Registry == nil {
		return nil, errors.New("gateway: Registry is required")
	}
	if opts.Logger == nil {
		return nil, errors.New("gateway: Logger is required")
	}

	transport := opts.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &Gateway{
		principals:  opts.Principals,
		leases:      opts.Leases,
		revocations: opts.Revocations,
		credentials: opts.Credentials,
		registry:    opts.Registry,
		recorder:    opts.Recorder,
		dashboard:   opts.Dashboard,
		logger:      opts.Logger,
		transport:   transport,
	}, nil
}

// Handler returns the gateway's HTTP surface.
//
// Everything not claimed by an operational endpoint is treated as proxy
// traffic, because vendors add routes faster than this package can enumerate
// them and an unrecognised path should produce a clear routing error rather
// than a 404 from the wrong layer.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", g.handleHealth)
	if g.dashboard != nil {
		g.dashboard.Routes(mux)
	} else {
		mux.HandleFunc("GET /{$}", g.handleRoot)
	}
	mux.Handle("/", g.authenticate(http.HandlerFunc(g.handleProxy)))

	return g.logRequests(mux)
}

// handleHealth reports liveness. It is deliberately unauthenticated and
// deliberately says nothing about internal state.
func (g *Gateway) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
}

// handleRoot serves a status page when no dashboard registrar was supplied.
//
// Returning something honest here matters: the quickstart tells people to open
// this port, and a blank 404 would read as a broken install rather than an
// unfinished feature.
func (g *Gateway) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, `spendlease

The gateway is running and proxying requests.

This process was started without the embedded dashboard. Available routes:

  GET  /healthz              liveness
  *    /v1/chat/completions  proxied to OpenAI
  *    /v1/messages          proxied to Anthropic

Point a vendor SDK at this address and authenticate with a principal key:

  client = OpenAI(base_url="http://localhost:4000/v1", api_key=SPENDLEASE_KEY)

Requests are metered and enforce-mode principals are capped. See `+DocsBase+`
`)
}

// contextKey is unexported so no other package can collide with these keys.
type contextKey int

const (
	ctxPrincipal contextKey = iota
	ctxInfo
	ctxRun
	ctxLease
	ctxLeaseObject
)

// runIDFrom returns the run this request is charged to.
func runIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRun).(string)
	return id
}

func leaseFrom(ctx context.Context) (store.Lease, bool) {
	l, ok := ctx.Value(ctxLeaseObject).(store.Lease)
	return l, ok
}

func leaseIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxLease).(string)
	return id
}

// requestInfo collects attribution as a request travels inward, so the
// logging middleware on the outside can report it.
//
// A pointer is needed rather than plain context values: each layer calls
// r.WithContext, which produces a new request the outer layer never sees. A
// shared struct is the only thing both ends can reach. Handlers finish before
// the log line is written, so no synchronisation is required.
type requestInfo struct {
	principalID string
	provider    string
	model       string
	runID       string
	mode        string
}

// infoFrom returns the attribution holder for this request, if there is one.
func infoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(ctxInfo).(*requestInfo)
	return info
}

// principalFrom returns the authenticated principal stored on the context.
func principalFrom(ctx context.Context) (store.Principal, bool) {
	p, ok := ctx.Value(ctxPrincipal).(store.Principal)
	return p, ok
}

// vendorKeyFor fetches the vendor credential for a provider, translating a
// missing credential into an actionable error.
func (g *Gateway) vendorKeyFor(ctx context.Context, name string) (string, *APIErrorDetail) {
	key, err := g.credentials.Get(ctx, name)
	if err == nil {
		return key, nil
	}

	if errors.Is(err, vault.ErrNoCredential) {
		return "", &APIErrorDetail{
			Type:     ErrTypeNoCredential,
			Provider: name,
			Message: fmt.Sprintf(
				"spendlease has no %s API key, so it cannot make this request on your behalf.", name),
			Resolution: fmt.Sprintf(
				"Store one with: spendlease keys provider set %s --key <your %s api key>", name, name),
			Docs: DocsBase + "/getting-started/",
		}
	}

	// A decryption failure is the other realistic case, and it has a specific
	// cause worth naming rather than hiding behind "internal error".
	if errors.Is(err, vault.ErrDecrypt) {
		return "", &APIErrorDetail{
			Type:     ErrTypeNoCredential,
			Provider: name,
			Message: fmt.Sprintf(
				"The stored %s credential could not be decrypted.", name),
			Resolution: "SPENDLEASE_MASTER_KEY does not match the one this database was written with. " +
				"Restore the original master key, or re-enter the vendor credentials under the new one.",
			Docs: DocsBase + "/self-hosting/",
		}
	}

	g.logger.Error("fetching vendor credential", "provider", name, "error", err)
	return "", &APIErrorDetail{
		Type:    ErrTypeInternal,
		Message: "spendlease could not read the stored vendor credential.",
	}
}
