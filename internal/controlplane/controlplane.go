// Package controlplane exposes the guarded JSON API used by orchestrators and
// operator SDKs. It manages runs and leases without giving those callers
// direct access to the SQLite file.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/dashboard"
	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

const maxJSONBody = 1 << 20

// Store is the persistence surface used by the control plane.
type Store interface {
	GetPrincipal(context.Context, string) (store.Principal, error)
	CreateRun(context.Context, store.Run) error
	GetRun(context.Context, string) (store.Run, error)
	ListRunsByPrincipal(context.Context, string) ([]store.Run, error)
	CloseRun(context.Context, string) error
	BudgetStatus(context.Context, string) (store.RunBudgetStatus, error)
	CreateLease(context.Context, store.Lease) error
	ListLeasesByRun(context.Context, string) ([]store.Lease, error)
	LedgerEntries(context.Context, store.LedgerFilter) ([]ledger.Entry, error)
}

// LeaseRevoker invalidates a lease in memory and in durable storage.
type LeaseRevoker interface {
	RevokeLease(context.Context, string) (store.Lease, error)
}

// Options configures the JSON control plane.
type Options struct {
	Store   Store
	Revoker LeaseRevoker
	Guard   dashboard.Guard
	Logger  *slog.Logger
}

// API serves guarded operator endpoints under /api/v1.
type API struct {
	store   Store
	revoker LeaseRevoker
	guard   dashboard.Guard
	logger  *slog.Logger
}

// New returns a ready control-plane API.
func New(opts Options) (*API, error) {
	if opts.Store == nil {
		return nil, errors.New("controlplane: Store is required")
	}
	if opts.Revoker == nil {
		return nil, errors.New("controlplane: Revoker is required")
	}
	if opts.Logger == nil {
		return nil, errors.New("controlplane: Logger is required")
	}
	return &API{store: opts.Store, revoker: opts.Revoker, guard: opts.Guard, logger: opts.Logger}, nil
}

// Routes registers every versioned JSON endpoint.
func (a *API) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/principals/{principal}/runs", a.guard.Protect(http.HandlerFunc(a.listRuns)))
	mux.Handle("POST /api/v1/principals/{principal}/runs", a.guard.Protect(http.HandlerFunc(a.createRun)))
	mux.Handle("GET /api/v1/runs/{run}", a.guard.Protect(http.HandlerFunc(a.getRun)))
	mux.Handle("POST /api/v1/runs/{run}/close", a.guard.Protect(http.HandlerFunc(a.closeRun)))
	mux.Handle("GET /api/v1/runs/{run}/budget", a.guard.Protect(http.HandlerFunc(a.getBudget)))
	mux.Handle("GET /api/v1/runs/{run}/leases", a.guard.Protect(http.HandlerFunc(a.listLeases)))
	mux.Handle("POST /api/v1/runs/{run}/leases", a.guard.Protect(http.HandlerFunc(a.issueLease)))
	mux.Handle("POST /api/v1/leases/{lease}/revoke", a.guard.Protect(http.HandlerFunc(a.revokeLease)))
	mux.Handle("GET /api/v1/ledger/verify", a.guard.Protect(http.HandlerFunc(a.verifyLedger)))
	mux.Handle("GET /api/v1/ledger/export", a.guard.Protect(http.HandlerFunc(a.exportLedger)))
}

type createRunRequest struct {
	BudgetUSD   string `json:"budget_usd"`
	ParentRunID string `json:"parent_run_id"`
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	principalID := r.PathValue("principal")
	if _, err := a.store.GetPrincipal(r.Context(), principalID); err != nil {
		a.storeError(w, "reading principal", err)
		return
	}
	var input createRunRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	budget, err := parseNonnegativeUSD(input.BudgetUSD)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "budget_usd must be a non-negative decimal USD string")
		return
	}
	run := store.Run{
		ID: store.NewRunID(), PrincipalID: principalID, ParentRunID: strings.TrimSpace(input.ParentRunID),
		Budget: budget, Status: store.RunActive, CreatedAt: time.Now().UTC(),
	}
	if err := a.store.CreateRun(r.Context(), run); err != nil {
		a.storeError(w, "creating run", err)
		return
	}
	writeJSON(w, http.StatusCreated, runResponse(run))
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	principalID := r.PathValue("principal")
	if _, err := a.store.GetPrincipal(r.Context(), principalID); err != nil {
		a.storeError(w, "reading principal", err)
		return
	}
	runs, err := a.store.ListRunsByPrincipal(r.Context(), principalID)
	if err != nil {
		a.storeError(w, "listing runs", err)
		return
	}
	out := make([]runDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, runResponse(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		a.storeError(w, "reading run", err)
		return
	}
	writeJSON(w, http.StatusOK, runResponse(run))
}

func (a *API) closeRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	if err := a.store.CloseRun(r.Context(), id); err != nil {
		a.storeError(w, "closing run", err)
		return
	}
	run, err := a.store.GetRun(r.Context(), id)
	if err != nil {
		a.storeError(w, "reading closed run", err)
		return
	}
	writeJSON(w, http.StatusOK, runResponse(run))
}

func (a *API) getBudget(w http.ResponseWriter, r *http.Request) {
	status, err := a.store.BudgetStatus(r.Context(), r.PathValue("run"))
	if err != nil {
		a.storeError(w, "reading remaining budget", err)
		return
	}
	levels := make([]budgetLevelDTO, 0, len(status.Levels))
	for _, level := range status.Levels {
		levels = append(levels, budgetLevelDTO{
			RunID: level.RunID, Status: level.Status, BudgetUSD: level.Budget.String(), SpentUSD: level.Spent.String(),
			HeldUSD: level.Held.String(), RemainingUSD: level.Remaining.String(), Unlimited: level.Unlimited,
		})
	}
	writeJSON(w, http.StatusOK, budgetDTO{
		RunID: status.RunID, Status: status.Status, SpendAllowed: status.SpendAllowed,
		BlockingRunID: status.BlockingRunID, Unlimited: status.Unlimited,
		EffectiveRemainingUSD: status.EffectiveRemaining.String(), LimitingRunID: status.LimitingRunID, Levels: levels,
	})
}

type issueLeaseRequest struct {
	TTLSeconds int64    `json:"ttl_seconds"`
	Providers  []string `json:"providers"`
	CeilingUSD string   `json:"ceiling_usd"`
}

func (a *API) issueLease(w http.ResponseWriter, r *http.Request) {
	run, err := a.store.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		a.storeError(w, "reading run", err)
		return
	}
	if run.Status != store.RunActive {
		writeAPIError(w, http.StatusConflict, "run_closed", "leases cannot be issued for a closed run")
		return
	}
	var input issueLeaseRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.TTLSeconds == 0 {
		input.TTLSeconds = int64((15 * time.Minute) / time.Second)
	}
	if input.TTLSeconds < 1 || input.TTLSeconds > int64((30*24*time.Hour)/time.Second) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "ttl_seconds must be between 1 and 2592000")
		return
	}
	ceiling, err := parseNonnegativeUSD(defaultString(input.CeilingUSD, "0"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "ceiling_usd must be a non-negative decimal USD string")
		return
	}
	scope, ok := normalizeProviders(input.Providers)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "providers must contain non-empty provider names without duplicates")
		return
	}
	token, hash := store.NewLeaseToken()
	now := time.Now().UTC()
	lease := store.Lease{
		ID: store.NewLeaseID(), RunID: run.ID, TokenHash: hash, Providers: scope, Ceiling: ceiling,
		ExpiresAt: now.Add(time.Duration(input.TTLSeconds) * time.Second), CreatedAt: now,
	}
	if err := a.store.CreateLease(r.Context(), lease); err != nil {
		a.storeError(w, "issuing lease", err)
		return
	}
	response := leaseResponse(lease, now)
	response.Token = token
	writeJSON(w, http.StatusCreated, response)
}

func (a *API) listLeases(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	if _, err := a.store.GetRun(r.Context(), runID); err != nil {
		a.storeError(w, "reading run", err)
		return
	}
	leases, err := a.store.ListLeasesByRun(r.Context(), runID)
	if err != nil {
		a.storeError(w, "listing leases", err)
		return
	}
	now := time.Now()
	out := make([]leaseDTO, 0, len(leases))
	for _, lease := range leases {
		out = append(out, leaseResponse(lease, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"leases": out})
}

func (a *API) revokeLease(w http.ResponseWriter, r *http.Request) {
	lease, err := a.revoker.RevokeLease(r.Context(), r.PathValue("lease"))
	if err != nil {
		a.storeError(w, "revoking lease", err)
		return
	}
	writeJSON(w, http.StatusOK, leaseResponse(lease, time.Now()))
}

func (a *API) verifyLedger(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.LedgerEntries(r.Context(), store.LedgerFilter{})
	if err != nil {
		a.storeError(w, "reading ledger", err)
		return
	}
	if err := ledger.VerifyChain(entries); err != nil {
		a.logger.Error("ledger verification failed", "error", err)
		writeAPIError(w, http.StatusConflict, "ledger_invalid", err.Error())
		return
	}
	result := map[string]any{"ok": true, "entries": len(entries), "head_hash": ledger.GenesisHash}
	if len(entries) > 0 {
		result["head_hash"] = entries[len(entries)-1].Hash
		result["head_sequence"] = entries[len(entries)-1].Seq
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) exportLedger(w http.ResponseWriter, r *http.Request) {
	filter, ok := ledgerFilter(w, r)
	if !ok {
		return
	}
	entries, err := a.store.LedgerEntries(r.Context(), filter)
	if err != nil {
		a.storeError(w, "exporting ledger", err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" || format == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="spendlease-ledger.json"`)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := ledger.WriteJSON(w, entries); err != nil {
			a.logger.Error("writing ledger JSON", "error", err)
		}
		return
	}
	if format != "csv" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "format must be json or csv")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="spendlease-ledger.csv"`)
	if err := ledger.WriteCSV(w, entries); err != nil {
		a.logger.Error("writing ledger CSV", "error", err)
	}
}

type runDTO struct {
	ID          string          `json:"id"`
	PrincipalID string          `json:"principal_id"`
	ParentRunID string          `json:"parent_run_id,omitempty"`
	BudgetUSD   string          `json:"budget_usd"`
	Status      store.RunStatus `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	ClosedAt    *time.Time      `json:"closed_at,omitempty"`
}

func runResponse(run store.Run) runDTO {
	return runDTO{run.ID, run.PrincipalID, run.ParentRunID, run.Budget.String(), run.Status, run.CreatedAt.UTC(), run.ClosedAt}
}

type leaseDTO struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	Providers  []string   `json:"providers"`
	CeilingUSD string     `json:"ceiling_usd"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Status     string     `json:"status"`
	Token      string     `json:"token,omitempty"`
}

func leaseResponse(lease store.Lease, now time.Time) leaseDTO {
	status := "active"
	if lease.RevokedAt != nil {
		status = "revoked"
	} else if !now.Before(lease.ExpiresAt) {
		status = "expired"
	}
	return leaseDTO{
		ID: lease.ID, RunID: lease.RunID, Providers: lease.Providers, CeilingUSD: lease.Ceiling.String(),
		ExpiresAt: lease.ExpiresAt.UTC(), RevokedAt: lease.RevokedAt, CreatedAt: lease.CreatedAt.UTC(), Status: status,
	}
}

type budgetDTO struct {
	RunID                 string           `json:"run_id"`
	Status                store.RunStatus  `json:"status"`
	SpendAllowed          bool             `json:"spend_allowed"`
	BlockingRunID         string           `json:"blocking_run_id,omitempty"`
	Unlimited             bool             `json:"unlimited"`
	EffectiveRemainingUSD string           `json:"effective_remaining_usd"`
	LimitingRunID         string           `json:"limiting_run_id,omitempty"`
	Levels                []budgetLevelDTO `json:"levels"`
}

type budgetLevelDTO struct {
	RunID        string          `json:"run_id"`
	Status       store.RunStatus `json:"status"`
	BudgetUSD    string          `json:"budget_usd"`
	SpentUSD     string          `json:"spent_usd"`
	HeldUSD      string          `json:"held_usd"`
	RemainingUSD string          `json:"remaining_usd"`
	Unlimited    bool            `json:"unlimited"`
}

func ledgerFilter(w http.ResponseWriter, r *http.Request) (store.LedgerFilter, bool) {
	filter := store.LedgerFilter{
		RunID:       strings.TrimSpace(r.URL.Query().Get("run_id")),
		PrincipalID: strings.TrimSpace(r.URL.Query().Get("principal_id")),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "since must be an RFC 3339 timestamp")
			return store.LedgerFilter{}, false
		}
		filter.Since = since
	}
	return filter, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must be one valid JSON object: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON object")
		return false
	}
	return true
}

func parseNonnegativeUSD(raw string) (money.Nanos, error) {
	value, err := money.ParseUSD(raw)
	if err != nil || value < 0 {
		return 0, money.ErrInvalidAmount
	}
	return value, nil
}

func normalizeProviders(raw []string) ([]string, bool) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" || seen[item] {
			return nil, false
		}
		seen[item] = true
		out = append(out, item)
	}
	return out, true
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("X-Spendlease-Error", kind)
	writeJSON(w, status, map[string]any{"error": map[string]string{"type": kind, "message": message}})
}

func (a *API) storeError(w http.ResponseWriter, doing string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "the requested resource does not exist")
	case errors.Is(err, store.ErrConflict):
		writeAPIError(w, http.StatusConflict, "conflict", err.Error())
	default:
		a.logger.Error(doing, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "spendlease could not complete the operation")
	}
}
