// Package store defines the persistence interface for spendlease and the
// domain objects it holds.
//
// There are four objects — principal, run, lease and reservation — plus the
// append-only ledger they produce. The model is deliberately small; see
// docs/concepts.md for what each one means and why the set is not larger.
//
// Backends live in subpackages. SQLite is the zero-configuration default;
// PostgreSQL provides the same authorization and ledger guarantees for
// multi-instance deployments.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
)

// Sentinel errors returned by every Store implementation. Callers should test
// with errors.Is rather than comparing backend-specific error strings.
var (
	// ErrNotFound means no row matched. It is returned rather than a nil
	// object so a missing principal cannot be mistaken for an unprivileged one.
	ErrNotFound = errors.New("store: not found")

	// ErrConflict means the write collided with an existing row, such as a
	// duplicate principal name or a reused key hash.
	ErrConflict = errors.New("store: conflict")

	// ErrImmutable means an attempt was made to modify or remove a ledger
	// entry. The database rejects this; the error exists so callers can
	// recognise it rather than treat it as a transient failure.
	ErrImmutable = errors.New("store: ledger is append-only")
)

// Mode controls whether a principal's spend limits actually block requests.
type Mode string

const (
	// ModeObserve records everything and blocks nothing. It is the default
	// for new principals, because nobody puts an unproven gateway in the
	// blocking path of production traffic.
	ModeObserve Mode = "observe"

	// ModeEnforce allows reservations to reject a request with 402.
	ModeEnforce Mode = "enforce"
)

// Valid reports whether m is a recognised mode.
func (m Mode) Valid() bool { return m == ModeObserve || m == ModeEnforce }

// RunStatus is the lifecycle state of a run.
type RunStatus string

const (
	// RunActive means the run may still spend.
	RunActive RunStatus = "active"
	// RunClosed means the run has finished; no new leases or reservations.
	RunClosed RunStatus = "closed"
)

// Valid reports whether s is a recognised run status.
func (s RunStatus) Valid() bool { return s == RunActive || s == RunClosed }

// ReservationStatus is the lifecycle state of a budget hold.
type ReservationStatus string

const (
	// ReservationPending is an in-flight hold against a run's budget.
	ReservationPending ReservationStatus = "pending"
	// ReservationSettled means the request completed and a ledger entry was
	// written; the difference between hold and actual has been released.
	ReservationSettled ReservationStatus = "settled"
	// ReservationReleased means the request failed and the full hold was
	// returned. Failures are never charged.
	ReservationReleased ReservationStatus = "released"
	// ReservationExpired means the hold outlived its TTL and was reclaimed by
	// the sweeper, typically after a client disconnected mid-stream.
	ReservationExpired ReservationStatus = "expired"
)

// Valid reports whether s is a recognised reservation status.
func (s ReservationStatus) Valid() bool {
	switch s {
	case ReservationPending, ReservationSettled, ReservationReleased, ReservationExpired:
		return true
	}
	return false
}

// Principal is a registered agent with a stable identity.
type Principal struct {
	// ID is the prefixed identifier, "prn_...".
	ID string
	// Name is a human-chosen label, unique within the deployment.
	Name string
	// KeyHash is the SHA-256 hex digest of the principal's slk_ key. The
	// plaintext key is shown once at creation and never stored.
	KeyHash string
	// Mode is observe or enforce.
	Mode Mode
	// CreatedAt is in UTC.
	CreatedAt time.Time
}

// Run is one execution of a principal, and the object that carries a budget.
type Run struct {
	// ID is the prefixed identifier, "run_...".
	ID string
	// PrincipalID is the agent this run executes as.
	PrincipalID string
	// ParentRunID is empty for a root run. A child run draws from its
	// parent's remaining budget; that is the entire delegation model.
	ParentRunID string
	// Budget is the ceiling for this run, in nanodollars.
	Budget money.Nanos
	// Status is active or closed.
	Status RunStatus
	// CreatedAt is in UTC.
	CreatedAt time.Time
	// ClosedAt is nil while the run is active.
	ClosedAt *time.Time
}

// IsRoot reports whether the run has no parent.
func (r Run) IsRoot() bool { return r.ParentRunID == "" }

// Lease is a short-lived scoped token issued against a run. It is what an
// agent holds in place of a vendor API key.
type Lease struct {
	// ID is the prefixed identifier, "lse_...".
	ID string
	// RunID is the run this lease spends against.
	RunID string
	// TokenHash is the SHA-256 hex digest of the sll_ token.
	TokenHash string
	// Providers scopes the lease to specific vendors, for example
	// {"openai", "anthropic"}. Empty means every configured provider.
	Providers []string
	// Ceiling caps what this one lease may spend, which may be lower than
	// the run's remaining budget.
	Ceiling money.Nanos
	// ExpiresAt is when the lease stops working regardless of budget left.
	ExpiresAt time.Time
	// RevokedAt is nil unless the lease has been explicitly revoked.
	RevokedAt *time.Time
	// CreatedAt is in UTC.
	CreatedAt time.Time
}

// Active reports whether the lease is usable at time now: not revoked and not
// expired.
func (l Lease) Active(now time.Time) bool {
	return l.RevokedAt == nil && now.Before(l.ExpiresAt)
}

// AllowsProvider reports whether the lease may reach the named provider. A
// lease with no provider scope allows every provider.
func (l Lease) AllowsProvider(name string) bool {
	if len(l.Providers) == 0 {
		return true
	}
	for _, p := range l.Providers {
		if p == name {
			return true
		}
	}
	return false
}

// Reservation is a pending hold against a run's budget for an in-flight
// request.
type Reservation struct {
	// ID is the prefixed identifier, "rsv_...".
	ID string
	// RunID is the run whose budget is held.
	RunID string
	// LeaseID is the lease that authorized the request. It is empty while the
	// transitional principal-key authentication path is in use; see ADR-0015.
	LeaseID string
	// Amount is the held upper bound, in nanodollars.
	Amount money.Nanos
	// Status is pending until the request resolves.
	Status ReservationStatus
	// ExpiresAt bounds how long the hold may survive. Without it, a client
	// that disconnects mid-stream would shrink the run's budget forever.
	ExpiresAt time.Time
	// CreatedAt is in UTC.
	CreatedAt time.Time
	// ResolvedAt is nil while the reservation is pending.
	ResolvedAt *time.Time
}

// BudgetDecision is the result of checking one reservation against its run
// and every budgeted ancestor.
type BudgetDecision struct {
	// Allowed reports whether the request may proceed in the principal's mode.
	Allowed bool
	// WouldBlock reports that at least one budget was insufficient. It is true
	// for observe-mode requests that are deliberately allowed through.
	WouldBlock bool
	// RunID is the first run whose budget was insufficient, or the target run
	// when every check passed.
	RunID string
	// Budget is RunID's configured ceiling. Zero means no ceiling.
	Budget money.Nanos
	// Spent is settled ledger spend in RunID's descendant subtree.
	Spent money.Nanos
	// Held is pending reservations in RunID's descendant subtree.
	Held money.Nanos
	// Requested is the new reservation amount.
	Requested money.Nanos
	// Remaining is what was available before this request.
	Remaining money.Nanos
	// Shortfall is Requested minus Remaining when WouldBlock is true.
	Shortfall money.Nanos
}

// BudgetLevel is one run in a target run's budget ancestry, with the settled
// and in-flight spend that currently consumes its ceiling.
type BudgetLevel struct {
	RunID     string
	Status    RunStatus
	Budget    money.Nanos
	Spent     money.Nanos
	Held      money.Nanos
	Remaining money.Nanos
	Unlimited bool
}

// RunBudgetStatus reports the effective budget available to a run after its
// own ceiling and every budgeted ancestor are considered.
type RunBudgetStatus struct {
	RunID              string
	Status             RunStatus
	SpendAllowed       bool
	BlockingRunID      string
	Unlimited          bool
	EffectiveRemaining money.Nanos
	LimitingRunID      string
	Levels             []BudgetLevel
}

// BudgetEvent is a durable record of a reservation that did not fit. Enforced
// events were rejected before egress; observe-mode events were forwarded but
// retain the decision an operator would otherwise only see in logs.
type BudgetEvent struct {
	PrincipalID string
	RunID       string
	LeaseID     string
	Provider    string
	Model       string
	Enforced    bool
	Requested   money.Nanos
	Remaining   money.Nanos
	Shortfall   money.Nanos
	CreatedAt   time.Time
}

// OperationalEventKind identifies one row in the dashboard's recent activity.
type OperationalEventKind string

const (
	// EventAllowed is a settled request recorded in the spend ledger.
	EventAllowed OperationalEventKind = "allowed"
	// EventBudgetBlocked is an enforced reservation rejected before egress.
	EventBudgetBlocked OperationalEventKind = "budget_blocked"
	// EventBudgetWouldBlock is an observe-mode reservation over its budget.
	EventBudgetWouldBlock OperationalEventKind = "budget_would_block"
	// EventLeaseRevoked marks a lease invalidated by an operator.
	EventLeaseRevoked OperationalEventKind = "lease_revoked"
	// EventLeaseExpired marks a lease whose validity window has ended.
	EventLeaseExpired OperationalEventKind = "lease_expired"
)

// OperationalEvent combines existing durable records into one dashboard
// timeline. It does not replace the ledger or lease tables.
type OperationalEvent struct {
	Kind          OperationalEventKind
	PrincipalID   string
	PrincipalName string
	RunID         string
	LeaseID       string
	Provider      string
	Model         string
	Amount        money.Nanos
	Remaining     money.Nanos
	CreatedAt     time.Time
}

// OperationalEventFilter narrows the dashboard's operational timeline.
// Zero values mean no constraint, except Limit which defaults in the store.
type OperationalEventFilter struct {
	// PrincipalID limits events to one agent.
	PrincipalID string
	// Kinds limits events to the selected result types.
	Kinds []OperationalEventKind
	// Query matches a run or lease ID.
	Query string
	// Since limits events to those at or after this instant.
	Since time.Time
	// Limit caps the number of newest rows returned.
	Limit int
}

// PrincipalSummary is one principal with its totals, as the dashboard shows
// it.
//
// It embeds Principal rather than repeating its fields, so a column added to
// the identity model appears here without a second edit.
type PrincipalSummary struct {
	Principal
	// Spend is everything charged to this principal, across all of its runs.
	Spend money.Nanos
	// Entries is how many ledger entries make up that total.
	Entries int
	// EstimatedEntries is how many of those were not priced from
	// vendor-reported usage. A high proportion means the total is a guess.
	EstimatedEntries int
	// Runs is how many runs the principal has.
	Runs int
	// OverBudgetRuns is how many of those have spent more than their budget.
	//
	// This is the historical "would have been blocked" signal for observe-mode
	// runs that actually settled above their budget.
	OverBudgetRuns int
	// Lease counts describe what credentials are usable now and why none are.
	ActiveLeases  int
	RevokedLeases int
	ExpiredLeases int
	// BudgetBlocks are enforce-mode denials. WouldBlockEvents are the same
	// decision observed without enforcement.
	BudgetBlocks     int
	WouldBlockEvents int
	// LastEvent includes successful calls, budget decisions, revocations and
	// expirations. LastActivity remains the most recent settled spend.
	LastEvent *time.Time
	// LastActivity is when it last spent anything, nil if never.
	LastActivity *time.Time
}

// RunSummary is one run with its totals.
type RunSummary struct {
	// ID is the run identifier.
	ID string
	// PrincipalID is the agent this run executes as.
	PrincipalID string
	// ParentRunID is empty for a root run.
	ParentRunID string
	// Budget is the ceiling recorded for this run.
	Budget money.Nanos
	// Spend is everything charged to it, excluding its children.
	Spend money.Nanos
	// Entries is how many ledger entries make up that total.
	Entries int
	// Status is active or closed.
	Status RunStatus
	// CreatedAt is in UTC.
	CreatedAt time.Time
	// LastActivity is when it last spent anything, nil if never.
	LastActivity *time.Time
}

// OverBudget reports whether the run has spent more than its budget allows.
//
// A budget of zero means unset rather than "no allowance", and is never over.
func (r RunSummary) OverBudget() bool {
	return r.Budget > 0 && r.Spend > r.Budget
}

// LedgerFilter narrows a ledger query. Zero values mean "no constraint", so
// an empty filter returns everything in sequence order.
type LedgerFilter struct {
	// RunID limits results to one run.
	RunID string
	// PrincipalID limits results to one principal.
	PrincipalID string
	// Since limits results to entries created at or after this instant.
	Since time.Time
	// Limit caps the number of rows returned. Zero means no cap.
	Limit int
}

// Store is the persistence interface. Every method takes a context and must
// respect its cancellation.
//
// Implementations must be safe for concurrent use by multiple goroutines.
type Store interface {
	// CreatePrincipal inserts a principal. It returns ErrConflict if the name
	// or key hash is already taken.
	CreatePrincipal(ctx context.Context, p Principal) error
	// GetPrincipal returns a principal by ID, or ErrNotFound.
	GetPrincipal(ctx context.Context, id string) (Principal, error)
	// GetPrincipalByKeyHash resolves an slk_ key hash to its principal. This
	// is the authentication path.
	GetPrincipalByKeyHash(ctx context.Context, keyHash string) (Principal, error)
	// ListPrincipals returns every principal, oldest first.
	ListPrincipals(ctx context.Context) ([]Principal, error)
	// SetPrincipalMode switches a principal between observe and enforce.
	SetPrincipalMode(ctx context.Context, id string, m Mode) error

	// CreateRun inserts a run. It returns ErrNotFound if the principal or the
	// named parent run does not exist.
	CreateRun(ctx context.Context, r Run) error
	// GetRun returns a run by ID, or ErrNotFound.
	GetRun(ctx context.Context, id string) (Run, error)
	// ListRunsByPrincipal returns a principal's runs, newest first.
	ListRunsByPrincipal(ctx context.Context, principalID string) ([]Run, error)
	// ChildRuns returns the direct children of a run.
	ChildRuns(ctx context.Context, parentRunID string) ([]Run, error)
	// CloseRun marks a run finished. Closing an already-closed run is a no-op.
	CloseRun(ctx context.Context, id string) error

	// CreateLease inserts a lease against an existing run.
	CreateLease(ctx context.Context, l Lease) error
	// GetLease returns a lease by ID, or ErrNotFound.
	GetLease(ctx context.Context, id string) (Lease, error)
	// GetLeaseByTokenHash resolves an sll_ token hash to its lease. This is
	// the per-request authorization path and must stay cheap.
	GetLeaseByTokenHash(ctx context.Context, tokenHash string) (Lease, error)
	// ListLeasesByRun returns a run's leases, newest first.
	ListLeasesByRun(ctx context.Context, runID string) ([]Lease, error)
	// RevokeLease invalidates one lease. Revoking twice is a no-op.
	RevokeLease(ctx context.Context, id string, at time.Time) error
	// RevokeLeasesForPrincipal invalidates every lease belonging to every run
	// of a principal, returning how many were newly revoked. This is the
	// durable half of the kill switch.
	RevokeLeasesForPrincipal(ctx context.Context, principalID string, at time.Time) (int, error)

	// CreateReservation inserts a pending hold.
	CreateReservation(ctx context.Context, r Reservation) error
	// TryReserve atomically checks the target run and every budgeted ancestor,
	// and inserts the hold when allowed. Observe mode passes enforce=false and
	// records a hold even when the result says WouldBlock.
	TryReserve(ctx context.Context, r Reservation, enforce bool) (BudgetDecision, error)
	// RecordBudgetEvent persists a reservation decision that did not fit.
	RecordBudgetEvent(ctx context.Context, e BudgetEvent) error
	// GetReservation returns a reservation by ID, or ErrNotFound.
	GetReservation(ctx context.Context, id string) (Reservation, error)
	// ResolveReservation moves a pending reservation to a terminal status.
	// Resolving one that is already resolved returns ErrConflict, so a double
	// settle cannot silently release budget twice.
	ResolveReservation(ctx context.Context, id string, status ReservationStatus, at time.Time) error
	// ExpirePendingReservations marks every pending reservation whose TTL has
	// passed as expired, returning how many were reclaimed.
	ExpirePendingReservations(ctx context.Context, now time.Time) (int, error)
	// PendingReservationTotal sums the still-held amounts for one run.
	PendingReservationTotal(ctx context.Context, runID string) (money.Nanos, error)
	// BudgetStatus reports current settled spend, holds and remaining budget
	// for a run and every ancestor that limits it.
	BudgetStatus(ctx context.Context, runID string) (RunBudgetStatus, error)
	// SettleReservation atomically appends one ledger entry and resolves its
	// reservation. Repeating the same settlement returns the original entry.
	SettleReservation(ctx context.Context, reservationID string, e ledger.Entry) (ledger.Entry, error)

	// AppendLedger seals an entry onto the end of the chain and persists it.
	// The store assigns Seq, PrevHash and Hash; any values already set on the
	// argument are ignored. The sealed entry is returned.
	AppendLedger(ctx context.Context, e ledger.Entry) (ledger.Entry, error)
	// LedgerEntries returns entries matching the filter, in sequence order.
	LedgerEntries(ctx context.Context, f LedgerFilter) ([]ledger.Entry, error)
	// LedgerHead returns the most recent entry. The bool is false when the
	// ledger is empty.
	LedgerHead(ctx context.Context) (ledger.Entry, bool, error)
	// SpendByRun totals what one run has been charged, excluding its children.
	SpendByRun(ctx context.Context, runID string) (money.Nanos, error)
	// SpendByPrincipal totals what one principal has been charged across all
	// of its runs.
	SpendByPrincipal(ctx context.Context, principalID string) (money.Nanos, error)
	// RecentOperationalEvents returns allowed, blocked, revoked and expired
	// activity newest first.
	RecentOperationalEvents(ctx context.Context, filter OperationalEventFilter, now time.Time) ([]OperationalEvent, error)

	// Close releases the underlying resources.
	Close() error
}
