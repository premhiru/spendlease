package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// DocsBase is where error messages point for further explanation.
const DocsBase = "https://premhiru.github.io/spendlease"

// Error types. These are stable identifiers a client can branch on; the
// message is for a human and may change.
const (
	// ErrTypeUnauthenticated means no usable credential was presented.
	ErrTypeUnauthenticated = "unauthenticated"
	// ErrTypeUnknownRoute means no provider claims the request path.
	ErrTypeUnknownRoute = "unknown_route"
	// ErrTypeUnknownRun means the run named by the caller does not exist or
	// belongs to a different principal.
	ErrTypeUnknownRun = "unknown_run"
	// ErrTypeBudgetExceeded means an enforce-mode reservation could not fit.
	ErrTypeBudgetExceeded = "budget_exceeded"
	// ErrTypeNoCredential means the gateway has no vendor key for the
	// provider the request was routed to.
	ErrTypeNoCredential = "provider_credential_missing"
	// ErrTypeUpstream means the vendor could not be reached.
	ErrTypeUpstream = "upstream_unavailable"
	// ErrTypeInternal means the gateway itself failed.
	ErrTypeInternal = "internal"
)

// APIError is the JSON body returned for every gateway-generated failure.
//
// Errors are a product surface. Each one names what went wrong, what the
// gateway was trying to do, and the specific next action — because the person
// reading it is debugging at an unreasonable hour and should not have to read
// the source to find out what "unauthorized" meant.
type APIError struct {
	Error APIErrorDetail `json:"error"`
}

// APIErrorDetail carries the details of a failure.
type APIErrorDetail struct {
	// Type is a stable machine-readable identifier.
	Type string `json:"type"`
	// Message explains the failure to a human.
	Message string `json:"message"`
	// Resolution is the concrete next step, when there is one.
	Resolution string `json:"resolution,omitempty"`
	// Provider is the vendor involved, when one had been resolved.
	Provider string `json:"provider,omitempty"`
	// Principal is the authenticated agent, when one had been resolved.
	Principal string `json:"principal,omitempty"`
	// Run is the run whose budget caused a rejection.
	Run string `json:"run,omitempty"`
	// Budget is Run's configured ceiling, as an exact USD decimal string.
	Budget string `json:"budget,omitempty"`
	// Spent is settled spend counted against Run.
	Spent string `json:"spent,omitempty"`
	// Held is pending reservations counted against Run.
	Held string `json:"held,omitempty"`
	// Requested is the rejected reservation amount.
	Requested string `json:"requested,omitempty"`
	// Remaining is the balance available before this request.
	Remaining string `json:"remaining,omitempty"`
	// Shortfall is the additional budget needed for this reservation.
	Shortfall string `json:"shortfall,omitempty"`
	// Docs links to the relevant documentation.
	Docs string `json:"docs,omitempty"`
}

// writeError sends a structured JSON error and logs it.
//
// The body is never a bare string: a client that gets JSON for success and
// plain text for failure has to special-case every error path.
func writeError(w http.ResponseWriter, logger *slog.Logger, status int, d APIErrorDetail) {
	w.Header().Set("Content-Type", "application/json")
	// This response comes from the gateway, not the vendor. Saying so saves
	// an engineer from searching a vendor's documentation for an error the
	// vendor never produced.
	w.Header().Set("X-Spendlease-Error", d.Type)
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(APIError{Error: d}); err != nil {
		logger.Error("writing error response", "error", err)
	}
}
