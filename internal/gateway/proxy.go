package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/store"
)

// maxRequestBody caps how much of a request body is read in order to measure
// it.
//
// Unlike a response, the request has to be buffered to inspect it before
// egress. A larger body is rejected in enforce mode and replayed unchanged,
// but explicitly marked unmetered, in observe mode.
const maxRequestBody = 8 << 20 // 8 MiB

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
	if lease, ok := leaseFrom(r.Context()); ok && !lease.AllowsProvider(provider.Name()) {
		writeError(w, g.logger, http.StatusForbidden, APIErrorDetail{
			Type: ErrTypeLeaseScopeDenied, Principal: principal.ID, Provider: provider.Name(),
			Message:    "This lease is not scoped for provider " + provider.Name() + ".",
			Resolution: "Issue a lease that includes this provider and retry.",
			Docs:       DocsBase + "/concepts/#lease",
		})
		return
	}

	billing := provider.Billing(r.Method, upstreamPath)
	if billing.Class == providers.BillingUnsupported && principal.Mode == store.ModeEnforce {
		writeUnenforceableSpend(w, g.logger, principal, provider.Name(), billing.Reason)
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
			status = http.StatusServiceUnavailable
		}
		writeError(w, g.logger, status, *apiErr)
		return
	}

	// Read the body so it can be measured, then hand a fresh reader to the
	// proxy. Doing this before anything is forwarded keeps the request the
	// vendor sees byte-identical to the one the caller sent.
	body, inspectable, err := readRequestBody(r)
	if err != nil {
		writeError(w, g.logger, http.StatusBadRequest, APIErrorDetail{
			Type:       ErrTypeInternal,
			Principal:  principal.ID,
			Message:    "spendlease could not read the request body.",
			Resolution: "Retry the request. If it persists, the body may exceed the size this gateway will accept.",
		})
		return
	}

	requestInfo := providers.RequestInfo{}
	if inspectable {
		requestInfo = provider.ParseRequest(body)
	}
	requestInfo.NoOutput = billing.NoOutput
	if info := infoFrom(ctx); info != nil {
		info.model = requestInfo.Model
	}

	metered := billing.Class == providers.BillingToken && inspectable && requestInfo.Model != "" &&
		requestInfo.UnsupportedBilling == ""
	unmeteredReason := ""
	switch {
	case billing.Class == providers.BillingUnsupported:
		unmeteredReason = billing.Reason
	case billing.Class == providers.BillingToken && !inspectable:
		unmeteredReason = "the request body is larger than the inspection limit"
	case billing.Class == providers.BillingToken && requestInfo.Model == "":
		unmeteredReason = "the request body is not valid, inspectable JSON with a model"
	case requestInfo.UnsupportedBilling != "":
		unmeteredReason = requestInfo.UnsupportedBilling
	}
	if unmeteredReason != "" {
		if principal.Mode == store.ModeEnforce {
			writeUnenforceableSpend(w, g.logger, principal, provider.Name(), unmeteredReason)
			return
		}
		g.logger.Warn("observe mode is forwarding spend that cannot be enforced",
			"principal", principal.ID, "provider", provider.Name(), "path", upstreamPath,
			"reason", unmeteredReason)
	}

	reservationID := ""
	if g.recorder != nil && metered {
		reservation, decision, reserveErr := g.recorder.Reserve(ctx, principal, provider.Name(), requestInfo)
		if reserveErr != nil {
			g.logger.Error("could not make budget decision",
				"principal", principal.ID, "run", runIDFrom(ctx), "error", reserveErr)
			if principal.Mode == store.ModeEnforce {
				writeError(w, g.logger, http.StatusInternalServerError, APIErrorDetail{
					Type:       ErrTypeInternal,
					Principal:  principal.ID,
					Run:        runIDFrom(ctx),
					Message:    "spendlease could not make an atomic budget decision, so enforcement failed closed.",
					Resolution: "Retry the request. If it persists, check the datastore and gateway logs before disabling enforcement.",
					Docs:       DocsBase + "/reserve-and-settle/",
				})
				return
			}
			g.logger.Warn("observe mode is forwarding without a reservation",
				"principal", principal.ID, "run", runIDFrom(ctx))
		} else if !decision.Allowed {
			writeBudgetExceeded(w, g.logger, principal, decision)
			return
		} else {
			reservationID = reservation.ID
			if decision.WouldBlock {
				g.logger.Warn("request would have exceeded budget",
					"principal", principal.ID, "run", decision.RunID,
					"requested", decision.Requested.String(), "shortfall", decision.Shortfall.String(),
					"mode", string(principal.Mode))
			}
		}
	}

	// Ask the vendor to report usage on a streamed response when the caller
	// did not. Without this an OpenAI-compatible stream cannot be priced
	// exactly, and the whole point of recording is that the numbers are real.
	//
	// This modifies the caller's request, which is surprising, so it is
	// announced on the response and documented prominently. The extra chunk
	// it produces is withheld below, so the stream the caller reads is the
	// one they would have got without spendlease in the path.
	injectedUsage := false
	if metered && requestInfo.Stream && !requestInfo.WantsUsage && len(body) > 0 {
		if modified, changed := provider.EnableStreamUsage(body); changed {
			body = modified
			injectedUsage = true
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			g.logger.Debug("enabled usage reporting on a streamed request",
				"principal", principal.ID, "provider", provider.Name(), "model", requestInfo.Model)
		}
	}

	base := provider.BaseURL()

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = base.Scheme
			pr.Out.URL.Host = base.Host
			pr.Out.URL.Path = strings.TrimSuffix(base.Path, "/") + upstreamPath
			pr.Out.Host = base.Host

			// Order matters: remove whatever the caller sent, then attach the
			// credential the gateway chose.
			stripClientCredentials(pr.Out.Header)
			provider.Authorize(pr.Out, vendorKey)

			pr.Out.Header.Del("Accept-Encoding")
		},

		// A negative flush interval means flush immediately after every write
		// to the client. This is what makes server-sent events actually
		// stream, and it is why accounting observes the body in passing
		// rather than buffering it.
		FlushInterval: -1,

		Transport: g.transport,

		// ReverseProxy owns the response body: it copies it to the client and
		// closes it afterwards. The hook wraps it without taking ownership.
		//nolint:bodyclose // the body is closed by ReverseProxy after copying
		ModifyResponse: g.modifyResponse(principal, provider, requestInfo, injectedUsage, reservationID, r, metered, unmeteredReason),

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if g.recorder != nil {
				g.recorder.Release(context.WithoutCancel(r.Context()), reservationID)
			}
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

func writeUnenforceableSpend(
	w http.ResponseWriter,
	logger *slog.Logger,
	p store.Principal,
	provider, reason string,
) {
	writeError(w, logger, http.StatusUnprocessableEntity, APIErrorDetail{
		Type:      ErrTypeSpendNotEnforceable,
		Principal: p.ID,
		Provider:  provider,
		Message:   "This request may incur spend that spendlease cannot conservatively reserve.",
		Resolution: "Use a supported text-token request, reduce the request below the inspection limit, " +
			"or switch to observe mode only if unmetered spend is acceptable.",
		Docs: DocsBase + "/pricing-book/",
	})
	logger.Warn("blocked spend that cannot be enforced",
		"principal", p.ID, "provider", provider, "reason", reason)
}

func writeBudgetExceeded(
	w http.ResponseWriter,
	logger *slog.Logger,
	p store.Principal,
	d store.BudgetDecision,
) {
	writeError(w, logger, http.StatusPaymentRequired, APIErrorDetail{
		Type:      ErrTypeBudgetExceeded,
		Principal: p.ID,
		Run:       d.RunID,
		Message: fmt.Sprintf(
			"run %s has $%s remaining, but this request needs a $%s reservation.",
			d.RunID, d.Remaining.String(), d.Requested.String()),
		Resolution: "Increase the run budget, reduce max_tokens, or switch the principal to observe mode while validating the estimate.",
		Budget:     d.Budget.String(),
		Spent:      d.Spent.String(),
		Held:       d.Held.String(),
		Requested:  d.Requested.String(),
		Remaining:  d.Remaining.String(),
		Shortfall:  d.Shortfall.String(),
		Docs:       DocsBase + "/reserve-and-settle/",
	})
}

// readRequestBody consumes and replaces the request body.
func readRequestBody(r *http.Request) ([]byte, bool, error) {
	if r.Body == nil {
		return nil, true, nil
	}

	original := r.Body
	// Read one byte past the observation limit so a body that is exactly the
	// limit remains measurable while a larger body can be identified without
	// consuming it all into memory.
	body, err := io.ReadAll(io.LimitReader(original, maxRequestBody+1))
	if err != nil {
		_ = original.Close()
		return nil, false, err
	}

	// Rewind for the proxy. For a large request the unread tail still belongs
	// to original, so keep that closer alive and prepend the bytes already
	// consumed. Closing original before building this reader would truncate
	// every request over the cap.
	if int64(len(body)) > maxRequestBody {
		r.Body = &replayReadCloser{
			Reader: io.MultiReader(bytes.NewReader(body), original),
			Closer: original,
		}
		return nil, false, nil // too large to inspect; still replayable in observe mode
	}
	_ = original.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return body, true, nil
}

func (g *Gateway) modifyResponse(
	principal store.Principal,
	provider providers.Provider,
	info providers.RequestInfo,
	injectedUsage bool,
	reservationID string,
	req *http.Request,
	metered bool,
	unmeteredReason string,
) func(*http.Response) error {
	if metered {
		// bodyclose cannot see that ReverseProxy owns the response body and
		// closes it after the returned hook wraps or observes it.
		//nolint:bodyclose // response body ownership remains with ReverseProxy
		return g.observeResponse(principal, provider, info, injectedUsage, reservationID, req)
	}
	if unmeteredReason == "" {
		return nil
	}
	return func(res *http.Response) error {
		res.Header.Set(AccountingHeader, "unmetered")
		return nil
	}
}

// replayReadCloser replays a consumed prefix before continuing with—and
// ultimately closing—the original request body.
type replayReadCloser struct {
	io.Reader
	io.Closer
}

// observeResponse returns a ModifyResponse hook that watches the vendor's
// reply and records what it cost.
//
// Nothing here changes the response. The body is wrapped in a reader that
// forwards every byte unchanged and reads usage out of the stream on the way
// past, so streaming behaves exactly as it did before accounting existed.
func (g *Gateway) observeResponse(
	principal store.Principal,
	provider providers.Provider,
	info providers.RequestInfo,
	injectedUsage bool,
	reservationID string,
	req *http.Request,
) func(*http.Response) error {
	if g.recorder == nil {
		return nil
	}

	return func(res *http.Response) error {
		upstreamOK := res.StatusCode >= 200 && res.StatusCode < 300

		// A failed request is not spend, and its body is an error message
		// rather than a completion. Leave it entirely alone.
		if !upstreamOK {
			g.recorder.Release(context.WithoutCancel(req.Context()), reservationID)
			return nil
		}

		streaming := isEventStream(res.Header.Get("Content-Type"))

		// Say so on the response. A modified request that leaves no trace is
		// the surprising kind; one that announces itself is discoverable from
		// a single curl -i.
		if injectedUsage {
			res.Header.Set(StreamUsageHeader, "injected")
		}

		var (
			mu    sync.Mutex
			usage providers.Usage
			seen  bool
		)

		obs := newObservingReader(res.Body, streaming)

		// Withhold the chunk the injection produced, so the caller's stream
		// looks exactly as it would have without spendlease. Only requests
		// spendlease modified take this path; everything else stays a
		// byte-for-byte pass-through.
		if injectedUsage && streaming {
			obs.drop = provider.IsUsageOnlyEvent
		}

		obs.onEvent = func(payload []byte) {
			if u, ok := provider.UsageFromStreamEvent(payload); ok {
				mu.Lock()
				usage.Merge(u)
				seen = true
				mu.Unlock()
			}
		}
		obs.onBody = func(body []byte) {
			if u, ok := provider.UsageFromResponse(body); ok {
				mu.Lock()
				usage.Merge(u)
				seen = true
				mu.Unlock()
			}
		}
		obs.onDone = func(complete bool) {
			mu.Lock()
			final, reported := usage, seen
			mu.Unlock()

			// The request context is already cancelled on a disconnect, so
			// recording uses a background context. Losing the entry for the
			// exact requests most worth recording would be perverse.
			g.record(context.WithoutCancel(req.Context()), observation{
				principal:     principal,
				provider:      provider.Name(),
				runID:         runIDFrom(req.Context()),
				request:       info,
				usage:         final,
				usageReported: reported,
				complete:      complete,
				upstreamOK:    true,
				reservationID: reservationID,
			})
		}

		res.Body = obs
		return nil
	}
}

// record forwards an observation to the recorder.
func (g *Gateway) record(ctx context.Context, obs observation) {
	if obs.runID == "" {
		// Run resolution failed earlier and was already reported. Recording
		// against no run is not possible, and inventing one would misattribute.
		return
	}
	g.recorder.Record(ctx, obs)
}

// isEventStream reports whether a content type denotes server-sent events.
func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(contentType)), "text/event-stream")
}
