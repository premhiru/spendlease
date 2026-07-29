package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/store"
)

// authenticate resolves the caller's credential to a principal and attaches
// it to the request context.
//
// In this phase the accepted credential is a principal key (slk_). Lease
// tokens (sll_) arrive with the lease phase and will be accepted here
// alongside them; the shape check below already tells the two apart so the
// error can say which one was presented.
func (g *Gateway) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := credentialFrom(r)
		if !ok {
			writeError(w, g.logger, http.StatusUnauthorized, APIErrorDetail{
				Type:    ErrTypeUnauthenticated,
				Message: "No spendlease credential was presented.",
				Resolution: "Send your principal key as `Authorization: Bearer slk_...`. " +
					"Most vendor SDKs do this for you when you set api_key to the principal key " +
					"and base_url to this gateway.",
				Docs: DocsBase + "/getting-started/",
			})
			return
		}

		if store.LooksLikeLeaseToken(presented) {
			hash := store.HashSecret(presented)
			if g.revocations != nil && g.revocations.Revoked(hash) {
				writeLeaseRejected(w, g, "That lease has been revoked.")
				return
			}
			if g.leases == nil {
				writeLeaseRejected(w, g, "Lease authentication is unavailable on this gateway.")
				return
			}
			lease, err := g.leases.GetLeaseByTokenHash(r.Context(), hash)
			if err != nil || !lease.Active(time.Now()) {
				writeLeaseRejected(w, g, "That lease is unknown, expired, or revoked.")
				return
			}
			run, err := g.leases.GetRun(r.Context(), lease.RunID)
			if err != nil || run.Status != store.RunActive {
				writeLeaseRejected(w, g, "The run behind that lease is unavailable or closed.")
				return
			}
			principal, err := g.leases.GetPrincipal(r.Context(), run.PrincipalID)
			if err != nil {
				writeLeaseRejected(w, g, "The principal behind that lease is unavailable.")
				return
			}
			ctx := context.WithValue(r.Context(), ctxPrincipal, principal)
			ctx = context.WithValue(ctx, ctxRun, run.ID)
			ctx = context.WithValue(ctx, ctxLease, lease.ID)
			ctx = context.WithValue(ctx, ctxLeaseObject, lease)
			if info := infoFrom(r.Context()); info != nil {
				info.principalID, info.runID, info.mode = principal.ID, run.ID, string(principal.Mode)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if !store.LooksLikePrincipalKey(presented) {
			writeError(w, g.logger, http.StatusUnauthorized, APIErrorDetail{
				Type:    ErrTypeUnauthenticated,
				Message: "The credential presented is not a spendlease key.",
				Resolution: "spendlease keys start with `slk_`. If you passed a vendor API key, " +
					"remove it: the gateway holds the vendor credential for you and the agent " +
					"never needs one.",
				Docs: DocsBase + "/concepts/",
			})
			return
		}

		principal, err := g.principals.GetPrincipalByKeyHash(r.Context(), store.HashSecret(presented))
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				g.logger.Error("resolving principal", "error", err)
			}
			// A wrong key and an unknown key are the same response. Saying
			// which would let a caller enumerate valid keys.
			writeError(w, g.logger, http.StatusUnauthorized, APIErrorDetail{
				Type:       ErrTypeUnauthenticated,
				Message:    "That principal key is not recognised.",
				Resolution: "Check the key, or create a new principal. Keys are shown once at creation and cannot be recovered.",
				Docs:       DocsBase + "/getting-started/",
			})
			return
		}

		if info := infoFrom(r.Context()); info != nil {
			info.principalID = principal.ID
			info.mode = string(principal.Mode)
		}

		ctx := context.WithValue(r.Context(), ctxPrincipal, principal)

		// Resolve the run now rather than at accounting time. A caller who
		// names a run that does not exist, or one belonging to somebody else,
		// should be told immediately instead of having the request succeed
		// and the spend land nowhere.
		if g.recorder != nil {
			runID, err := g.recorder.resolveRun(ctx, principal, r.Header.Get(RunHeader))
			if err != nil {
				g.logger.Warn("could not resolve run",
					"principal", principal.ID, "requested", r.Header.Get(RunHeader), "error", err)
				writeError(w, g.logger, http.StatusBadRequest, APIErrorDetail{
					Type:      ErrTypeUnknownRun,
					Principal: principal.ID,
					Message: fmt.Sprintf("The run %q named in %s cannot be used by this principal.",
						r.Header.Get(RunHeader), RunHeader),
					Resolution: "Omit the header to charge the principal's default run, or name a run that " +
						"belongs to this principal.",
					Docs: DocsBase + "/concepts/",
				})
				return
			}
			ctx = context.WithValue(ctx, ctxRun, runID)
			if info := infoFrom(r.Context()); info != nil {
				info.runID = runID
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeLeaseRejected(w http.ResponseWriter, g *Gateway, message string) {
	writeError(w, g.logger, http.StatusUnauthorized, APIErrorDetail{
		Type: ErrTypeUnauthenticated, Message: message,
		Resolution: "Issue a fresh lease with `spendlease keys lease issue` and retry.",
		Docs:       DocsBase + "/concepts/#lease",
	})
}

// credentialFrom extracts the presented credential from a request.
//
// Both header shapes are accepted because vendor SDKs differ: OpenAI clients
// send `Authorization: Bearer`, Anthropic clients send `x-api-key`. Supporting
// both is what lets a single base-URL override work for either SDK.
func credentialFrom(r *http.Request) (string, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, found := strings.CutPrefix(h, "Bearer "); found {
			if v := strings.TrimSpace(after); v != "" {
				return v, true
			}
		}
		// A bare Authorization value with no scheme is common enough in
		// hand-rolled clients to be worth accepting.
		if v := strings.TrimSpace(h); v != "" && !strings.Contains(v, " ") {
			return v, true
		}
	}
	if v := strings.TrimSpace(r.Header.Get("x-api-key")); v != "" {
		return v, true
	}
	return "", false
}

// stripClientCredentials removes every credential the client sent, before the
// request is forwarded upstream.
//
// This is load-bearing. The caller's spendlease key must never reach a vendor,
// and a vendor key the caller supplied must never be honoured — the whole
// point is that the gateway decides which credential goes out.
func stripClientCredentials(h http.Header) {
	h.Del("Authorization")
	h.Del("x-api-key")
	h.Del("api-key")
	h.Del("X-Api-Key")
}
