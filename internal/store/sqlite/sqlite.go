// Package sqlite implements store.Store on SQLite.
//
// It uses modernc.org/sqlite, a pure-Go translation of SQLite with no cgo and
// no libc dependency. That choice is what lets spendlease ship as a single
// static binary in an 8MB scratch container, which in turn is what makes the
// zero-configuration quickstart possible. It is the default backend and needs
// no setup: point it at a file path and it creates and migrates itself.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	// Registers the "sqlite" driver.
	_ "modernc.org/sqlite"

	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

// InMemory is a path that opens a private in-memory database, used by tests.
// Each call to Open with this path gets its own independent database.
const InMemory = ":memory:"

// Store is a SQLite-backed store.Store.
type Store struct {
	db     *sql.DB
	logger *slog.Logger

	// appendMu serialises ledger appends within this process.
	//
	// Sealing an entry requires reading the current head hash and writing the
	// next entry atomically with respect to other appends. SQLite gives us a
	// single writer, but two goroutines could still read the same head before
	// either writes, and produce two entries claiming the same predecessor.
	//
	// spendlease is deployed as one process against one database, so a mutex
	// is the right tool. If two processes ever did share a file, the primary
	// key on seq and the unique index on hash would turn the race into a
	// failed insert rather than a forked chain.
	appendMu sync.Mutex

	// reserveMu serialises budget decisions, releases, expiry and settlement
	// within this process. The decision itself still runs in a transaction;
	// the mutex prevents two goroutines using separate deferred transactions
	// from reading the same balance before either writes its hold.
	reserveMu sync.Mutex
}

// Options configures a SQLite store.
type Options struct {
	// Logger receives migration and lifecycle events. Defaults to a discard
	// logger, so tests stay quiet unless they ask not to be.
	Logger *slog.Logger
}

// Open opens or creates the database at path, applies any pending migrations,
// and returns a ready store. Passing InMemory gives a private throwaway
// database.
//
// The connection is configured with foreign keys enforced, write-ahead
// logging, and a busy timeout, because none of those are SQLite defaults and
// all three are wrong to omit in a service.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	logger := opts.Logger
	if logger == nil {
		// slog.DiscardHandler would be tidier but arrived in Go 1.24, and
		// go.mod still targets 1.22.
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database at %s: %w", path, err)
	}

	// An in-memory database lives only as long as its connection, so the pool
	// must hold exactly one or each query could land on a different, empty
	// database. For file-backed databases a single writer is also the honest
	// model, and SQLite serialises writers regardless.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to sqlite database at %s: %w", path, err)
	}

	if err := migrate(ctx, db, logger); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating sqlite database at %s: %w", path, err)
	}

	return &Store{db: db, logger: logger}, nil
}

// dsn builds a modernc.org/sqlite connection string with the pragmas this
// service requires.
func dsn(path string) string {
	q := url.Values{}
	// Off by default in SQLite, which would silently allow orphaned runs and
	// leases pointing at principals that do not exist.
	q.Add("_pragma", "foreign_keys(1)")
	// Readers do not block the writer; without this a dashboard query can
	// stall a proxied request.
	q.Add("_pragma", "journal_mode(WAL)")
	// Wait rather than failing instantly when another connection holds the
	// write lock.
	q.Add("_pragma", "busy_timeout(5000)")
	// Durable enough for a service, without an fsync on every commit.
	q.Add("_pragma", "synchronous(NORMAL)")

	if path == InMemory {
		return "file::memory:?" + q.Encode()
	}
	return "file:" + path + "?" + q.Encode()
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle. It exists for tests that need to assert
// on raw SQL behaviour, such as proving the ledger triggers reject writes.
func (s *Store) DB() *sql.DB { return s.db }

// ---------------------------------------------------------------------------
// Principals
// ---------------------------------------------------------------------------

// CreatePrincipal inserts a principal.
func (s *Store) CreatePrincipal(ctx context.Context, p store.Principal) error {
	if !p.Mode.Valid() {
		return fmt.Errorf("%w: mode %q is not observe or enforce", store.ErrConflict, p.Mode)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO principals (id, name, key_hash, mode, created_at) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.KeyHash, string(p.Mode), formatTime(p.CreatedAt),
	)
	return wrap(err, "creating principal")
}

// GetPrincipal returns a principal by ID.
func (s *Store) GetPrincipal(ctx context.Context, id string) (store.Principal, error) {
	return s.principalWhere(ctx, "id = ?", id)
}

// GetPrincipalByKeyHash resolves an slk_ key hash to its principal.
func (s *Store) GetPrincipalByKeyHash(ctx context.Context, keyHash string) (store.Principal, error) {
	return s.principalWhere(ctx, "key_hash = ?", keyHash)
}

func (s *Store) principalWhere(ctx context.Context, where string, arg any) (store.Principal, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, key_hash, mode, created_at FROM principals WHERE `+where, arg)

	var p store.Principal
	var mode, created string
	if err := row.Scan(&p.ID, &p.Name, &p.KeyHash, &mode, &created); err != nil {
		return store.Principal{}, wrap(err, "reading principal")
	}
	p.Mode = store.Mode(mode)

	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return store.Principal{}, err
	}
	return p, nil
}

// ListPrincipals returns every principal, oldest first.
func (s *Store) ListPrincipals(ctx context.Context) ([]store.Principal, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, key_hash, mode, created_at FROM principals ORDER BY created_at, id`)
	if err != nil {
		return nil, wrap(err, "listing principals")
	}
	// The close error is deliberately discarded: rows.Err() below reports any
	// failure that actually affected the results.
	defer func() { _ = rows.Close() }()

	var out []store.Principal
	for rows.Next() {
		var p store.Principal
		var mode, created string
		if err := rows.Scan(&p.ID, &p.Name, &p.KeyHash, &mode, &created); err != nil {
			return nil, wrap(err, "scanning principal")
		}
		p.Mode = store.Mode(mode)
		if p.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, wrap(rows.Err(), "iterating principals")
}

// SetPrincipalMode switches a principal between observe and enforce.
func (s *Store) SetPrincipalMode(ctx context.Context, id string, m store.Mode) error {
	if !m.Valid() {
		return fmt.Errorf("%w: mode %q is not observe or enforce", store.ErrConflict, m)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE principals SET mode = ? WHERE id = ?`, string(m), id)
	if err != nil {
		return wrap(err, "setting principal mode")
	}
	return requireAffected(res, "principal", id)
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

// CreateRun inserts a run.
func (s *Store) CreateRun(ctx context.Context, r store.Run) error {
	if !r.Status.Valid() {
		return fmt.Errorf("%w: status %q is not active or closed", store.ErrConflict, r.Status)
	}
	if r.ParentRunID != "" {
		parent, err := s.GetRun(ctx, r.ParentRunID)
		if err != nil {
			return err
		}
		if parent.PrincipalID != r.PrincipalID {
			return fmt.Errorf("%w: parent run belongs to a different principal", store.ErrConflict)
		}
		if parent.Status != store.RunActive {
			return fmt.Errorf("%w: parent run %s is closed", store.ErrConflict, parent.ID)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (id, principal_id, parent_run_id, budget_nanos, status, created_at, closed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.PrincipalID, nullString(r.ParentRunID), int64(r.Budget),
		string(r.Status), formatTime(r.CreatedAt), nullTime(r.ClosedAt),
	)
	return wrap(err, "creating run")
}

// GetRun returns a run by ID.
func (s *Store) GetRun(ctx context.Context, id string) (store.Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, principal_id, parent_run_id, budget_nanos, status, created_at, closed_at
		 FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

// ListRunsByPrincipal returns a principal's runs, newest first.
func (s *Store) ListRunsByPrincipal(ctx context.Context, principalID string) ([]store.Run, error) {
	return s.runsQuery(ctx,
		`SELECT id, principal_id, parent_run_id, budget_nanos, status, created_at, closed_at
		 FROM runs WHERE principal_id = ? ORDER BY created_at DESC, id`, principalID)
}

// ChildRuns returns the direct children of a run.
func (s *Store) ChildRuns(ctx context.Context, parentRunID string) ([]store.Run, error) {
	return s.runsQuery(ctx,
		`SELECT id, principal_id, parent_run_id, budget_nanos, status, created_at, closed_at
		 FROM runs WHERE parent_run_id = ? ORDER BY created_at, id`, parentRunID)
}

func (s *Store) runsQuery(ctx context.Context, q string, args ...any) ([]store.Run, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrap(err, "listing runs")
	}
	// The close error is deliberately discarded: rows.Err() below reports any
	// failure that actually affected the results.
	defer func() { _ = rows.Close() }()

	var out []store.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, wrap(rows.Err(), "iterating runs")
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanRun(sc scanner) (store.Run, error) {
	var r store.Run
	var parent, closed sql.NullString
	var status, created string
	var budget int64

	if err := sc.Scan(&r.ID, &r.PrincipalID, &parent, &budget, &status, &created, &closed); err != nil {
		return store.Run{}, wrap(err, "reading run")
	}

	r.ParentRunID = parent.String
	r.Budget = money.Nanos(budget)
	r.Status = store.RunStatus(status)

	var err error
	if r.CreatedAt, err = parseTime(created); err != nil {
		return store.Run{}, err
	}
	if r.ClosedAt, err = parseNullTime(closed); err != nil {
		return store.Run{}, err
	}
	return r, nil
}

// CloseRun marks a run finished. Closing an already-closed run is a no-op.
func (s *Store) CloseRun(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'closed', closed_at = ?
		 WHERE id = ? AND status = 'active'`,
		formatTime(time.Now()), id)
	if err != nil {
		return wrap(err, "closing run")
	}

	n, err := res.RowsAffected()
	if err != nil {
		return wrap(err, "closing run")
	}
	if n > 0 {
		return nil
	}
	// Nothing changed: either the run is already closed, or it does not exist.
	// Only the second is an error.
	if _, err := s.GetRun(ctx, id); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Leases
// ---------------------------------------------------------------------------

// CreateLease inserts a lease against an existing run.
func (s *Store) CreateLease(ctx context.Context, l store.Lease) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO leases (id, run_id, token_hash, providers, ceiling_nanos, expires_at, revoked_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.RunID, l.TokenHash, encodeProviders(l.Providers), int64(l.Ceiling),
		formatTime(l.ExpiresAt), nullTime(l.RevokedAt), formatTime(l.CreatedAt),
	)
	return wrap(err, "creating lease")
}

// GetLease returns a lease by ID.
func (s *Store) GetLease(ctx context.Context, id string) (store.Lease, error) {
	return s.leaseWhere(ctx, "id = ?", id)
}

// GetLeaseByTokenHash resolves an sll_ token hash to its lease.
func (s *Store) GetLeaseByTokenHash(ctx context.Context, tokenHash string) (store.Lease, error) {
	return s.leaseWhere(ctx, "token_hash = ?", tokenHash)
}

func (s *Store) leaseWhere(ctx context.Context, where string, arg any) (store.Lease, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, token_hash, providers, ceiling_nanos, expires_at, revoked_at, created_at
		 FROM leases WHERE `+where, arg)
	return scanLease(row)
}

// ListLeasesByRun returns a run's leases, newest first.
func (s *Store) ListLeasesByRun(ctx context.Context, runID string) ([]store.Lease, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, token_hash, providers, ceiling_nanos, expires_at, revoked_at, created_at
		 FROM leases WHERE run_id = ? ORDER BY created_at DESC, id`, runID)
	if err != nil {
		return nil, wrap(err, "listing leases")
	}
	// The close error is deliberately discarded: rows.Err() below reports any
	// failure that actually affected the results.
	defer func() { _ = rows.Close() }()

	var out []store.Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, wrap(rows.Err(), "iterating leases")
}

func scanLease(sc scanner) (store.Lease, error) {
	var l store.Lease
	var providers, expires, created string
	var revoked sql.NullString
	var ceiling int64

	if err := sc.Scan(&l.ID, &l.RunID, &l.TokenHash, &providers, &ceiling, &expires, &revoked, &created); err != nil {
		return store.Lease{}, wrap(err, "reading lease")
	}

	l.Providers = decodeProviders(providers)
	l.Ceiling = money.Nanos(ceiling)

	var err error
	if l.ExpiresAt, err = parseTime(expires); err != nil {
		return store.Lease{}, err
	}
	if l.CreatedAt, err = parseTime(created); err != nil {
		return store.Lease{}, err
	}
	if l.RevokedAt, err = parseNullTime(revoked); err != nil {
		return store.Lease{}, err
	}
	return l, nil
}

// RevokeLease invalidates one lease. Revoking twice is a no-op: the first
// revocation time is kept, because when a lease stopped working is a fact
// worth preserving.
func (s *Store) RevokeLease(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE leases SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(at), id)
	if err != nil {
		return wrap(err, "revoking lease")
	}

	n, err := res.RowsAffected()
	if err != nil {
		return wrap(err, "revoking lease")
	}
	if n > 0 {
		return nil
	}
	if _, err := s.GetLease(ctx, id); err != nil {
		return err
	}
	return nil
}

// RevokeLeasesForPrincipal invalidates every lease belonging to every run of
// a principal, returning how many were newly revoked.
func (s *Store) RevokeLeasesForPrincipal(ctx context.Context, principalID string, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE leases SET revoked_at = ?
		 WHERE revoked_at IS NULL
		   AND run_id IN (SELECT id FROM runs WHERE principal_id = ?)`,
		formatTime(at), principalID)
	if err != nil {
		return 0, wrap(err, "revoking leases for principal")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrap(err, "revoking leases for principal")
	}
	return int(n), nil
}

// ---------------------------------------------------------------------------
// Reservations
// ---------------------------------------------------------------------------

// CreateReservation inserts a pending hold.
func (s *Store) CreateReservation(ctx context.Context, r store.Reservation) error {
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()

	return s.createReservation(ctx, s.db, r)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) createReservation(ctx context.Context, db execer, r store.Reservation) error {
	if !r.Status.Valid() {
		return fmt.Errorf("%w: reservation status %q is not recognised", store.ErrConflict, r.Status)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO reservations (id, run_id, lease_id, amount_nanos, status, expires_at, created_at, resolved_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.RunID, nullString(r.LeaseID), int64(r.Amount), string(r.Status),
		formatTime(r.ExpiresAt), formatTime(r.CreatedAt), nullTime(r.ResolvedAt),
	)
	return wrap(err, "creating reservation")
}

// TryReserve checks every applicable budget and conditionally inserts a hold
// in one transaction.
func (s *Store) TryReserve(ctx context.Context, r store.Reservation, enforce bool) (store.BudgetDecision, error) {
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()

	decision := store.BudgetDecision{RunID: r.RunID, Requested: r.Amount}
	if r.Status != store.ReservationPending {
		return decision, fmt.Errorf("%w: a new reservation must be pending", store.ErrConflict)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return decision, wrap(err, "beginning budget decision")
	}
	defer func() { _ = tx.Rollback() }()

	type node struct {
		id     string
		budget money.Nanos
		status store.RunStatus
	}
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE ancestors(id, parent_run_id, budget_nanos, status, depth) AS (
			SELECT id, parent_run_id, budget_nanos, status, 0 FROM runs WHERE id = ?
			UNION ALL
			SELECT r.id, r.parent_run_id, r.budget_nanos, r.status, a.depth + 1
			FROM runs r JOIN ancestors a ON r.id = a.parent_run_id
		)
		SELECT id, budget_nanos, status FROM ancestors ORDER BY depth`, r.RunID)
	if err != nil {
		return decision, wrap(err, "reading run budget chain")
	}

	var chain []node
	for rows.Next() {
		var n node
		var budget int64
		var status string
		if err := rows.Scan(&n.id, &budget, &status); err != nil {
			_ = rows.Close()
			return decision, wrap(err, "reading run budget chain")
		}
		n.budget = money.Nanos(budget)
		n.status = store.RunStatus(status)
		chain = append(chain, n)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return decision, wrap(err, "iterating run budget chain")
	}
	if err := rows.Close(); err != nil {
		return decision, wrap(err, "closing run budget chain")
	}
	if len(chain) == 0 {
		return decision, fmt.Errorf("%w: run %s", store.ErrNotFound, r.RunID)
	}

	for _, n := range chain {
		if n.status != store.RunActive {
			return decision, fmt.Errorf("%w: run %s is %s", store.ErrConflict, n.id, n.status)
		}
		if n.budget == 0 {
			continue
		}

		spent, held, err := subtreeUsage(ctx, tx, n.id)
		if err != nil {
			return decision, err
		}
		remaining := n.budget
		if spent >= remaining {
			remaining = 0
		} else {
			remaining -= spent
		}
		if held >= remaining {
			remaining = 0
		} else {
			remaining -= held
		}

		if r.Amount > remaining && !decision.WouldBlock {
			decision.WouldBlock = true
			decision.RunID = n.id
			decision.Budget = n.budget
			decision.Spent = spent
			decision.Held = held
			decision.Remaining = remaining
			decision.Shortfall = r.Amount - remaining
		}
	}

	decision.Allowed = !enforce || !decision.WouldBlock
	if decision.Allowed {
		if err := s.createReservation(ctx, tx, r); err != nil {
			return decision, err
		}
	}
	if err := tx.Commit(); err != nil {
		return decision, wrap(err, "committing budget decision")
	}
	return decision, nil
}

func subtreeUsage(ctx context.Context, tx *sql.Tx, runID string) (money.Nanos, money.Nanos, error) {
	var spent, held int64
	err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT id FROM runs WHERE id = ?
			UNION ALL
			SELECT r.id FROM runs r JOIN subtree s ON r.parent_run_id = s.id
		)
		SELECT
			COALESCE((SELECT SUM(cost_nanos) FROM ledger WHERE run_id IN (SELECT id FROM subtree)), 0),
			COALESCE((SELECT SUM(amount_nanos) FROM reservations
			          WHERE status = 'pending' AND run_id IN (SELECT id FROM subtree)), 0)`, runID).
		Scan(&spent, &held)
	if err != nil {
		return 0, 0, wrap(err, "calculating subtree budget usage")
	}
	return money.Nanos(spent), money.Nanos(held), nil
}

// GetReservation returns a reservation by ID.
func (s *Store) GetReservation(ctx context.Context, id string) (store.Reservation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, lease_id, amount_nanos, status, expires_at, created_at, resolved_at
		 FROM reservations WHERE id = ?`, id)

	var r store.Reservation
	var status, expires, created string
	var lease sql.NullString
	var resolved sql.NullString
	var amount int64

	if err := row.Scan(&r.ID, &r.RunID, &lease, &amount, &status, &expires, &created, &resolved); err != nil {
		return store.Reservation{}, wrap(err, "reading reservation")
	}

	r.LeaseID = lease.String
	r.Amount = money.Nanos(amount)
	r.Status = store.ReservationStatus(status)

	var err error
	if r.ExpiresAt, err = parseTime(expires); err != nil {
		return store.Reservation{}, err
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return store.Reservation{}, err
	}
	if r.ResolvedAt, err = parseNullTime(resolved); err != nil {
		return store.Reservation{}, err
	}
	return r, nil
}

// ResolveReservation moves a pending reservation to a terminal status.
//
// Resolving one that is no longer pending returns ErrConflict rather than
// succeeding quietly. A double settle would release the same budget twice,
// and silently tolerating it would make that bug invisible.
func (s *Store) ResolveReservation(ctx context.Context, id string, status store.ReservationStatus, at time.Time) error {
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()

	if !status.Valid() || status == store.ReservationPending {
		return fmt.Errorf("%w: %q is not a terminal reservation status", store.ErrConflict, status)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE reservations SET status = ?, resolved_at = ?
		 WHERE id = ? AND status = 'pending'`,
		string(status), formatTime(at), id)
	if err != nil {
		return wrap(err, "resolving reservation")
	}

	n, err := res.RowsAffected()
	if err != nil {
		return wrap(err, "resolving reservation")
	}
	if n > 0 {
		return nil
	}

	existing, err := s.GetReservation(ctx, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: reservation %s is already %s", store.ErrConflict, id, existing.Status)
}

// ExpirePendingReservations reclaims every pending hold whose TTL has passed.
func (s *Store) ExpirePendingReservations(ctx context.Context, now time.Time) (int, error) {
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`UPDATE reservations SET status = 'expired', resolved_at = ?
		 WHERE status = 'pending' AND expires_at <= ?`,
		formatTime(now), formatTime(now))
	if err != nil {
		return 0, wrap(err, "expiring reservations")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrap(err, "expiring reservations")
	}
	return int(n), nil
}

// PendingReservationTotal sums the still-held amounts for one run.
func (s *Store) PendingReservationTotal(ctx context.Context, runID string) (money.Nanos, error) {
	var total sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT SUM(amount_nanos) FROM reservations WHERE run_id = ? AND status = 'pending'`,
		runID).Scan(&total)
	if err != nil {
		return 0, wrap(err, "summing pending reservations")
	}
	return money.Nanos(total.Int64), nil
}

// SettleReservation atomically appends a charge and resolves its hold.
func (s *Store) SettleReservation(
	ctx context.Context,
	reservationID string,
	e ledger.Entry,
) (ledger.Entry, error) {
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()
	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.Entry{}, wrap(err, "beginning reservation settlement")
	}
	defer func() { _ = tx.Rollback() }()

	var existingSeq int64
	err = tx.QueryRowContext(ctx,
		`SELECT ledger_seq FROM reservation_settlements WHERE reservation_id = ?`,
		reservationID).Scan(&existingSeq)
	if err == nil {
		return ledgerEntryBySeq(ctx, tx, existingSeq)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, wrap(err, "checking prior reservation settlement")
	}

	var runID, status string
	if err := tx.QueryRowContext(ctx,
		`SELECT run_id, status FROM reservations WHERE id = ?`, reservationID).
		Scan(&runID, &status); err != nil {
		return ledger.Entry{}, wrap(err, "reading reservation for settlement")
	}
	if e.RunID != runID {
		return ledger.Entry{}, fmt.Errorf("%w: reservation %s belongs to run %s, not %s",
			store.ErrConflict, reservationID, runID, e.RunID)
	}
	if status != string(store.ReservationPending) && status != string(store.ReservationExpired) {
		return ledger.Entry{}, fmt.Errorf("%w: reservation %s is already %s",
			store.ErrConflict, reservationID, status)
	}

	sealed, err := appendLedgerTx(ctx, tx, e)
	if err != nil {
		return ledger.Entry{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO reservation_settlements (reservation_id, ledger_seq, created_at)
		 VALUES (?, ?, ?)`, reservationID, sealed.Seq, formatTime(time.Now())); err != nil {
		return ledger.Entry{}, wrap(err, "linking reservation settlement")
	}
	if status == string(store.ReservationPending) {
		if _, err := tx.ExecContext(ctx,
			`UPDATE reservations SET status = 'settled', resolved_at = ? WHERE id = ?`,
			formatTime(time.Now()), reservationID); err != nil {
			return ledger.Entry{}, wrap(err, "resolving settled reservation")
		}
	}

	if err := tx.Commit(); err != nil {
		return ledger.Entry{}, wrap(err, "committing reservation settlement")
	}
	return sealed, nil
}

func ledgerEntryBySeq(ctx context.Context, tx *sql.Tx, seq int64) (ledger.Entry, error) {
	return scanEntry(tx.QueryRowContext(ctx,
		`SELECT seq, run_id, principal_id, provider, model,
		        input_tokens, output_tokens, cost_nanos, estimated,
		        created_at, prev_hash, hash
		 FROM ledger WHERE seq = ?`, seq))
}

// ---------------------------------------------------------------------------
// Ledger
// ---------------------------------------------------------------------------

// AppendLedger seals an entry onto the end of the chain and persists it.
func (s *Store) AppendLedger(ctx context.Context, e ledger.Entry) (ledger.Entry, error) {
	s.appendMu.Lock()
	defer s.appendMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.Entry{}, wrap(err, "beginning ledger append")
	}
	defer func() { _ = tx.Rollback() }()

	sealed, err := appendLedgerTx(ctx, tx, e)
	if err != nil {
		return ledger.Entry{}, err
	}

	if err := tx.Commit(); err != nil {
		return ledger.Entry{}, wrap(err, "committing ledger append")
	}
	return sealed, nil
}

func appendLedgerTx(ctx context.Context, tx *sql.Tx, e ledger.Entry) (ledger.Entry, error) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	e.CreatedAt = e.CreatedAt.UTC()

	var headSeq int64
	prev := ledger.GenesisHash
	row := tx.QueryRowContext(ctx, `SELECT seq, hash FROM ledger ORDER BY seq DESC LIMIT 1`)
	var headHash string
	switch err := row.Scan(&headSeq, &headHash); {
	case err == nil:
		prev = headHash
	case errors.Is(err, sql.ErrNoRows):
		// Empty ledger: the first entry chains onto the genesis hash.
	default:
		return ledger.Entry{}, wrap(err, "reading ledger head")
	}

	e.Seq = headSeq + 1
	sealed := e.Seal(prev)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ledger (seq, run_id, principal_id, provider, model,
		                     input_tokens, output_tokens, cost_nanos, estimated,
		                     created_at, prev_hash, hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sealed.Seq, sealed.RunID, sealed.PrincipalID, sealed.Provider, sealed.Model,
		sealed.InputTokens, sealed.OutputTokens, int64(sealed.Cost), boolToInt(sealed.Estimated),
		formatTime(sealed.CreatedAt), sealed.PrevHash, sealed.Hash,
	); err != nil {
		return ledger.Entry{}, wrap(err, "appending ledger entry")
	}
	return sealed, nil
}

// LedgerEntries returns entries matching the filter, in sequence order.
func (s *Store) LedgerEntries(ctx context.Context, f store.LedgerFilter) ([]ledger.Entry, error) {
	q := strings.Builder{}
	q.WriteString(`SELECT seq, run_id, principal_id, provider, model,
	                      input_tokens, output_tokens, cost_nanos, estimated,
	                      created_at, prev_hash, hash
	               FROM ledger`)

	var (
		where []string
		args  []any
	)
	if f.RunID != "" {
		where = append(where, "run_id = ?")
		args = append(args, f.RunID)
	}
	if f.PrincipalID != "" {
		where = append(where, "principal_id = ?")
		args = append(args, f.PrincipalID)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, formatTime(f.Since))
	}
	if len(where) > 0 {
		q.WriteString(" WHERE " + strings.Join(where, " AND "))
	}

	q.WriteString(" ORDER BY seq")
	if f.Limit > 0 {
		q.WriteString(" LIMIT ?")
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, wrap(err, "listing ledger entries")
	}
	// The close error is deliberately discarded: rows.Err() below reports any
	// failure that actually affected the results.
	defer func() { _ = rows.Close() }()

	var out []ledger.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, wrap(rows.Err(), "iterating ledger entries")
}

// LedgerHead returns the most recent entry, if there is one.
func (s *Store) LedgerHead(ctx context.Context) (ledger.Entry, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT seq, run_id, principal_id, provider, model,
		        input_tokens, output_tokens, cost_nanos, estimated,
		        created_at, prev_hash, hash
		 FROM ledger ORDER BY seq DESC LIMIT 1`)

	e, err := scanEntry(row)
	if errors.Is(err, store.ErrNotFound) {
		return ledger.Entry{}, false, nil
	}
	if err != nil {
		return ledger.Entry{}, false, err
	}
	return e, true, nil
}

func scanEntry(sc scanner) (ledger.Entry, error) {
	var e ledger.Entry
	var created string
	var cost int64
	var estimated int

	if err := sc.Scan(&e.Seq, &e.RunID, &e.PrincipalID, &e.Provider, &e.Model,
		&e.InputTokens, &e.OutputTokens, &cost, &estimated,
		&created, &e.PrevHash, &e.Hash); err != nil {
		return ledger.Entry{}, wrap(err, "reading ledger entry")
	}

	e.Cost = money.Nanos(cost)
	e.Estimated = estimated != 0

	var err error
	if e.CreatedAt, err = parseTime(created); err != nil {
		return ledger.Entry{}, err
	}
	return e, nil
}

// SpendByRun totals what one run has been charged, excluding its children.
func (s *Store) SpendByRun(ctx context.Context, runID string) (money.Nanos, error) {
	return s.sum(ctx, `SELECT SUM(cost_nanos) FROM ledger WHERE run_id = ?`, runID)
}

// SpendByPrincipal totals what one principal has been charged across all runs.
func (s *Store) SpendByPrincipal(ctx context.Context, principalID string) (money.Nanos, error) {
	return s.sum(ctx, `SELECT SUM(cost_nanos) FROM ledger WHERE principal_id = ?`, principalID)
}

func (s *Store) sum(ctx context.Context, q string, args ...any) (money.Nanos, error) {
	var total sql.NullInt64
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&total); err != nil {
		return 0, wrap(err, "summing ledger")
	}
	return money.Nanos(total.Int64), nil
}

// Verify compiles only if *Store satisfies the interface it claims to.
var _ store.Store = (*Store)(nil)
