package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/premhiru/spendlease/internal/providers"
)

// handleProxy forwards an authenticated request to its vendor.
func (g *Gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())

	provider, upstreamPath, err := g.registry.Resolve(r)
	if err != nil {
		if errors.Is(err, providers.ErrUnknownRoute) {
			writeError(w, g.logger, http.StatusNotFound, APIErrorDetail{
				Type:      ErrTypeUnknownRoute,
				Principal: principal.ID,
				Message: fmt.Sprintf(
					"No provider handles %s, so spendlease does not know where to send this request.", r.URL.Path),
				Resolution: fmt.Sprintf(
					"Known providers: %s. Point your SDK's base URL at this gateway's root, or force a "+
						"provider with an explicit prefix such as /%s%s.",
					strings.Join(g.registry.Names(), ", "),
					g.registry.Names()[0], r.URL.Path),
				Docs: DocsBase + "/api-reference/",
			})
			return
		}
		g.logger.Error("resolving provider", "error", err)
		writeError(w, g.logger, http.StatusInternalServerError, APIErrorDetail{
			Type:    ErrTypeInternal,
			Message: "spendlease could not route this request.",
		})
		return
	}

	if info := infoFrom(r.Context()); info != nil {
		info.provider = provider.Name()
	}
	ctx := r.Context()

	vendorKey, apiErr := g.vendorKeyFor(ctx, provider.Name())
	if apiErr != nil {
		apiErr.Principal = principal.ID
		status := http.StatusBadGateway
		if apiErr.Type == ErrTypeNoCredential {
			// The gateway is configured wrongly, not broken, and the caller
			// cannot fix it by retrying.
			status = http.StatusServiceUnavailable
		}
		writeError(w, g.logger, status, *apiErr)
		return
	}

	base := provider.BaseURL()

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = base.Scheme
			pr.Out.URL.Host = base.Host
			pr.Out.URL.Path = strings.TrimSuffix(base.Path, "/") + upstreamPath
			pr.Out.Host = base.Host

			// Order matters: remove whatever the caller sent, then attach the
			// credential the gateway chose. Doing it the other way round
			// would let a caller's header survive.
			stripClientCredentials(pr.Out.Header)
			provider.Authorize(pr.Out, vendorKey)

			// Hop-by-hop headers that would confuse the upstream.
			pr.Out.Header.Del("Accept-Encoding")
		},

		// A negative flush interval means flush immediately after every write
		// to the client. This is what makes server-sent events actually
		// stream: without it the proxy may buffer, and an agent waiting on
		// first-token latency would see nothing until the response completed.
		FlushInterval: -1,

		Transport: g.transport,

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// A client that hangs up mid-stream is normal, not an error worth
			// alarming about. Later phases settle the reservation from
			// partial usage; here it is only worth a debug line.
			if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
				g.logger.Debug("client disconnected",
					"provider", provider.Name(), "principal", principal.ID, "path", r.URL.Path)
				return
			}

			g.logger.Error("upstream request failed",
				"provider", provider.Name(), "principal", principal.ID,
				"path", r.URL.Path, "error", err)

			writeError(w, g.logger, http.StatusBadGateway, APIErrorDetail{
				Type:      ErrTypeUpstream,
				Provider:  provider.Name(),
				Principal: principal.ID,
				Message:   fmt.Sprintf("spendlease could not reach %s.", provider.Name()),
				Resolution: "This is a failure between spendlease and the vendor, not a problem with your request. " +
					"Check the vendor's status page and retry.",
				Docs: DocsBase + "/api-reference/",
			})
		},
	}

	proxy.ServeHTTP(w, r)
}
