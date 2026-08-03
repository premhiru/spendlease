package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

// newTestStore returns an isolated in-memory store, closed when the test ends.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(context.Background(), InMemory, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// seedPrincipal creates a principal and returns it.
func seedPrincipal(t *testing.T, s *Store, name string) store.Principal {
	t.Helper()

	_, hash := store.NewPrincipalKey()
	p := store.Principal{
		ID:        store.NewPrincipalID(),
		Name:      name,
		KeyHash:   hash,
		Mode:      store.ModeObserve,
		CreatedAt: time.Now(),
	}
	if err := s.CreatePrincipal(context.Background(), p); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	return p
}

// seedRun creates a run under a principal, optionally under a parent run.
func seedRun(t *testing.T, s *Store, principalID, parentID string, budget money.Nanos) store.Run {
	t.Helper()

	r := store.Run{
		ID:          store.NewRunID(),
		PrincipalID: principalID,
		ParentRunID: parentID,
		Budget:      budget,
		Status:      store.RunActive,
		CreatedAt:   time.Now(),
	}
	if err := s.CreateRun(context.Background(), r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return r
}

// seedLease creates a lease against a run.
func seedLease(t *testing.T, s *Store, runID string, ttl time.Duration) store.Lease {
	t.Helper()

	_, hash := store.NewLeaseToken()
	l := store.Lease{
		ID:        store.NewLeaseID(),
		RunID:     runID,
		TokenHash: hash,
		Providers: []string{"openai"},
		Ceiling:   money.MustParseUSD("5.00"),
		ExpiresAt: time.Now().Add(ttl),
		CreatedAt: time.Now(),
	}
	if err := s.CreateLease(context.Background(), l); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return l
}

// ---------------------------------------------------------------------------
// Schema and migrations
// ---------------------------------------------------------------------------

func TestMigrationsAreIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	path := dir + "/spendlease.db"

	for i := 0; i < 3; i++ {
		s, err := Open(ctx, path, Options{})
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}

	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("final Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	var applied int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("counting migrations: %v", err)
	}
	if want := len(mustLoadMigrations(t)); applied != want {
		t.Errorf("applied %d migrations after 4 opens, want %d", applied, want)
	}
}

func mustLoadMigrations(t *testing.T) []migration {
	t.Helper()

	m, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(m) == 0 {
		t.Fatal("no migrations were embedded")
	}
	return m
}

// TestForeignKeysAreEnforced guards the pragma. SQLite leaves foreign keys off
// by default, which would silently allow runs pointing at principals that do
// not exist.
func TestForeignKeysAreEnforced(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	err := s.CreateRun(context.Background(), store.Run{
		ID:          store.NewRunID(),
		PrincipalID: "prn_does_not_exist",
		Budget:      money.MustParseUSD("1.00"),
		Status:      store.RunActive,
		CreatedAt:   time.Now(),
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CreateRun with a missing principal: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Principals
// ---------------------------------------------------------------------------

func TestPrincipalLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "checkout-agent")

	t.Run("new principals default to observe", func(t *testing.T) {
		got, err := s.GetPrincipal(ctx, p.ID)
		if err != nil {
			t.Fatalf("GetPrincipal: %v", err)
		}
		if got.Mode != store.ModeObserve {
			t.Errorf("mode = %q, want observe", got.Mode)
		}
		if got.Name != "checkout-agent" {
			t.Errorf("name = %q", got.Name)
		}
	})

	t.Run("resolves by key hash", func(t *testing.T) {
		got, err := s.GetPrincipalByKeyHash(ctx, p.KeyHash)
		if err != nil {
			t.Fatalf("GetPrincipalByKeyHash: %v", err)
		}
		if got.ID != p.ID {
			t.Errorf("id = %q, want %q", got.ID, p.ID)
		}
	})

	t.Run("switches to enforce", func(t *testing.T) {
		if err := s.SetPrincipalMode(ctx, p.ID, store.ModeEnforce); err != nil {
			t.Fatalf("SetPrincipalMode: %v", err)
		}
		got, _ := s.GetPrincipal(ctx, p.ID)
		if got.Mode != store.ModeEnforce {
			t.Errorf("mode = %q, want enforce", got.Mode)
		}
	})

	t.Run("rejects an unknown mode", func(t *testing.T) {
		if err := s.SetPrincipalMode(ctx, p.ID, store.Mode("audit")); err == nil {
			t.Error("SetPrincipalMode accepted an invalid mode")
		}
	})

	t.Run("missing principal is ErrNotFound", func(t *testing.T) {
		if _, err := s.GetPrincipal(ctx, "prn_nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
		if err := s.SetPrincipalMode(ctx, "prn_nope", store.ModeEnforce); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})
}

func TestPrincipalUniqueness(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	first := seedPrincipal(t, s, "duplicate")

	tests := []struct {
		name string
		p    store.Principal
	}{
		{
			name: "duplicate name",
			p: store.Principal{
				ID: store.NewPrincipalID(), Name: "duplicate",
				KeyHash: store.HashSecret("something-else"),
				Mode:    store.ModeObserve, CreatedAt: time.Now(),
			},
		},
		{
			name: "duplicate key hash",
			p: store.Principal{
				ID: store.NewPrincipalID(), Name: "different-name",
				KeyHash: first.KeyHash,
				Mode:    store.ModeObserve, CreatedAt: time.Now(),
			},
		},
		{
			name: "duplicate id",
			p: store.Principal{
				ID: first.ID, Name: "another-name",
				KeyHash: store.HashSecret("another-secret"),
				Mode:    store.ModeObserve, CreatedAt: time.Now(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.CreatePrincipal(ctx, tt.p); !errors.Is(err, store.ErrConflict) {
				t.Errorf("got %v, want ErrConflict", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

func TestRunDelegation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "orchestrator")

	parent := seedRun(t, s, p.ID, "", money.MustParseUSD("25.00"))
	childA := seedRun(t, s, p.ID, parent.ID, money.MustParseUSD("5.00"))
	childB := seedRun(t, s, p.ID, parent.ID, money.MustParseUSD("5.00"))

	t.Run("root run has no parent", func(t *testing.T) {
		got, err := s.GetRun(ctx, parent.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if !got.IsRoot() {
			t.Errorf("parent run reports ParentRunID = %q, want empty", got.ParentRunID)
		}
		if got.Budget != money.MustParseUSD("25.00") {
			t.Errorf("budget = %s, want 25.00", got.Budget)
		}
	})

	t.Run("children are linked to the parent", func(t *testing.T) {
		kids, err := s.ChildRuns(ctx, parent.ID)
		if err != nil {
			t.Fatalf("ChildRuns: %v", err)
		}
		if len(kids) != 2 {
			t.Fatalf("got %d children, want 2", len(kids))
		}
		found := map[string]bool{}
		for _, k := range kids {
			found[k.ID] = true
			if k.ParentRunID != parent.ID {
				t.Errorf("child %s has parent %q", k.ID, k.ParentRunID)
			}
		}
		if !found[childA.ID] || !found[childB.ID] {
			t.Error("ChildRuns did not return both children")
		}
	})

	t.Run("a run cannot be its own parent", func(t *testing.T) {
		id := store.NewRunID()
		err := s.CreateRun(ctx, store.Run{
			ID: id, PrincipalID: p.ID, ParentRunID: id,
			Budget: money.MustParseUSD("1.00"), Status: store.RunActive, CreatedAt: time.Now(),
		})
		if err == nil {
			t.Error("a self-parented run was accepted")
		}
	})

	t.Run("a missing parent is rejected", func(t *testing.T) {
		err := s.CreateRun(ctx, store.Run{
			ID: store.NewRunID(), PrincipalID: p.ID, ParentRunID: "run_ghost",
			Budget: money.MustParseUSD("1.00"), Status: store.RunActive, CreatedAt: time.Now(),
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})
}

func TestCloseRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "worker")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))

	if err := s.CloseRun(ctx, r.ID); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	got, err := s.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != store.RunClosed {
		t.Errorf("status = %q, want closed", got.Status)
	}
	if got.ClosedAt == nil {
		t.Error("ClosedAt is nil on a closed run")
	}

	// Closing twice is a no-op, and must not clear the original close time.
	firstClose := *got.ClosedAt
	if err := s.CloseRun(ctx, r.ID); err != nil {
		t.Errorf("second CloseRun: %v", err)
	}
	again, _ := s.GetRun(ctx, r.ID)
	if !again.ClosedAt.Equal(firstClose) {
		t.Error("closing twice changed the original close time")
	}

	if err := s.CloseRun(ctx, "run_ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("closing a missing run: got %v, want ErrNotFound", err)
	}
}

func TestCreateLeaseRefusesClosedRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedPrincipal(t, s, "closed-lease")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))
	if err := s.CloseRun(ctx, r.ID); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	_, hash := store.NewLeaseToken()
	err := s.CreateLease(ctx, store.Lease{
		ID: store.NewLeaseID(), RunID: r.ID, TokenHash: hash,
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("CreateLease error = %v, want conflict", err)
	}
}

func TestCreateLeaseRefusesClosedAncestor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedPrincipal(t, s, "closed-parent-lease")
	parent := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))
	child := seedRun(t, s, p.ID, parent.ID, money.MustParseUSD("1.00"))
	if err := s.CloseRun(ctx, parent.ID); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	_, hash := store.NewLeaseToken()
	err := s.CreateLease(ctx, store.Lease{
		ID: store.NewLeaseID(), RunID: child.ID, TokenHash: hash,
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), parent.ID) {
		t.Fatalf("CreateLease error = %v, want conflict naming closed parent", err)
	}
}

// ---------------------------------------------------------------------------
// Leases
// ---------------------------------------------------------------------------

func TestLeaseLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))
	l := seedLease(t, s, r.ID, 15*time.Minute)

	t.Run("resolves by token hash and keeps its scope", func(t *testing.T) {
		got, err := s.GetLeaseByTokenHash(ctx, l.TokenHash)
		if err != nil {
			t.Fatalf("GetLeaseByTokenHash: %v", err)
		}
		if got.ID != l.ID {
			t.Errorf("id = %q, want %q", got.ID, l.ID)
		}
		if len(got.Providers) != 1 || got.Providers[0] != "openai" {
			t.Errorf("providers = %v, want [openai]", got.Providers)
		}
		if !got.AllowsProvider("openai") {
			t.Error("lease should allow openai")
		}
		if got.AllowsProvider("anthropic") {
			t.Error("lease should not allow anthropic")
		}
	})

	t.Run("is active before expiry", func(t *testing.T) {
		got, _ := s.GetLease(ctx, l.ID)
		if !got.Active(time.Now()) {
			t.Error("fresh lease reports inactive")
		}
		if got.Active(time.Now().Add(time.Hour)) {
			t.Error("lease still active an hour past its TTL")
		}
	})

	t.Run("revocation is recorded and idempotent", func(t *testing.T) {
		at := time.Now()
		if err := s.RevokeLease(ctx, l.ID, at); err != nil {
			t.Fatalf("RevokeLease: %v", err)
		}

		got, _ := s.GetLease(ctx, l.ID)
		if got.RevokedAt == nil {
			t.Fatal("RevokedAt is nil after revocation")
		}
		if got.Active(time.Now()) {
			t.Error("revoked lease still reports active")
		}

		first := *got.RevokedAt
		if err := s.RevokeLease(ctx, l.ID, at.Add(time.Hour)); err != nil {
			t.Fatalf("second RevokeLease: %v", err)
		}
		again, _ := s.GetLease(ctx, l.ID)
		if !again.RevokedAt.Equal(first) {
			t.Error("re-revoking overwrote the original revocation time")
		}
	})
}

// TestRevokeLeasesForPrincipal covers the durable half of the kill switch: one
// call must invalidate every lease the principal owns, across all of its runs.
func TestRevokeLeasesForPrincipal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)

	target := seedPrincipal(t, s, "runaway")
	bystander := seedPrincipal(t, s, "innocent")

	runA := seedRun(t, s, target.ID, "", money.MustParseUSD("10.00"))
	runB := seedRun(t, s, target.ID, runA.ID, money.MustParseUSD("5.00"))
	otherRun := seedRun(t, s, bystander.ID, "", money.MustParseUSD("10.00"))

	seedLease(t, s, runA.ID, time.Hour)
	seedLease(t, s, runA.ID, time.Hour)
	seedLease(t, s, runB.ID, time.Hour)
	expired := seedLease(t, s, runB.ID, -time.Hour)
	untouched := seedLease(t, s, otherRun.ID, time.Hour)

	n, err := s.RevokeLeasesForPrincipal(ctx, target.ID, time.Now())
	if err != nil {
		t.Fatalf("RevokeLeasesForPrincipal: %v", err)
	}
	if n != 3 {
		t.Errorf("revoked %d leases, want 3", n)
	}
	stillExpired, _ := s.GetLease(ctx, expired.ID)
	if stillExpired.RevokedAt != nil {
		t.Error("an already-expired lease was relabeled as revoked")
	}

	for _, runID := range []string{runA.ID, runB.ID} {
		leases, _ := s.ListLeasesByRun(ctx, runID)
		for _, l := range leases {
			if l.Active(time.Now()) {
				t.Errorf("lease %s in run %s survived revocation", l.ID, runID)
			}
		}
	}

	other, _ := s.GetLease(ctx, untouched.ID)
	if !other.Active(time.Now()) {
		t.Error("another principal's lease was revoked")
	}

	// A second call revokes nothing new.
	if n, err := s.RevokeLeasesForPrincipal(ctx, target.ID, time.Now()); err != nil || n != 0 {
		t.Errorf("second revoke = (%d, %v), want (0, nil)", n, err)
	}
}

// ---------------------------------------------------------------------------
// Reservations
// ---------------------------------------------------------------------------

func TestReservationResolution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	terminal := []store.ReservationStatus{
		store.ReservationSettled,
		store.ReservationReleased,
		store.ReservationExpired,
	}

	for _, status := range terminal {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			s := newTestStore(t)
			p := seedPrincipal(t, s, "agent-"+string(status))
			r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))
			l := seedLease(t, s, r.ID, time.Hour)

			res := store.Reservation{
				ID: store.NewReservationID(), RunID: r.ID, LeaseID: l.ID,
				Amount: money.MustParseUSD("1.50"), Status: store.ReservationPending,
				ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
			}
			if err := s.CreateReservation(ctx, res); err != nil {
				t.Fatalf("CreateReservation: %v", err)
			}

			total, err := s.PendingReservationTotal(ctx, r.ID)
			if err != nil {
				t.Fatalf("PendingReservationTotal: %v", err)
			}
			if total != money.MustParseUSD("1.50") {
				t.Errorf("pending total = %s, want 1.50", total)
			}

			if err := s.ResolveReservation(ctx, res.ID, status, time.Now()); err != nil {
				t.Fatalf("ResolveReservation: %v", err)
			}

			got, _ := s.GetReservation(ctx, res.ID)
			if got.Status != status {
				t.Errorf("status = %q, want %q", got.Status, status)
			}
			if got.ResolvedAt == nil {
				t.Error("ResolvedAt is nil after resolution")
			}

			// The hold is no longer counted against the run.
			if total, _ := s.PendingReservationTotal(ctx, r.ID); !total.IsZero() {
				t.Errorf("pending total after resolution = %s, want 0", total)
			}
		})
	}
}

// TestDoubleSettleIsRejected is the important one. Settling twice would
// release the same budget twice, quietly inflating what a run can spend.
func TestDoubleSettleIsRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))
	l := seedLease(t, s, r.ID, time.Hour)

	res := store.Reservation{
		ID: store.NewReservationID(), RunID: r.ID, LeaseID: l.ID,
		Amount: money.MustParseUSD("2.00"), Status: store.ReservationPending,
		ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	}
	if err := s.CreateReservation(ctx, res); err != nil {
		t.Fatalf("CreateReservation: %v", err)
	}

	if err := s.ResolveReservation(ctx, res.ID, store.ReservationSettled, time.Now()); err != nil {
		t.Fatalf("first settle: %v", err)
	}

	err := s.ResolveReservation(ctx, res.ID, store.ReservationSettled, time.Now())
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second settle: got %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "already settled") {
		t.Errorf("error %q does not say what state it was already in", err)
	}

	// Releasing an already-settled reservation is equally refused.
	if err := s.ResolveReservation(ctx, res.ID, store.ReservationReleased, time.Now()); !errors.Is(err, store.ErrConflict) {
		t.Errorf("release after settle: got %v, want ErrConflict", err)
	}
}

func TestResolveRejectsPendingAsTarget(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	err := s.ResolveReservation(context.Background(), "rsv_x", store.ReservationPending, time.Now())
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("got %v, want ErrConflict for a non-terminal target status", err)
	}
}

// TestExpirePendingReservations covers the sweeper that reclaims holds
// orphaned by a client disconnecting mid-stream.
func TestExpirePendingReservations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("100.00"))
	l := seedLease(t, s, r.ID, time.Hour)

	now := time.Now()
	mk := func(amount string, expiresIn time.Duration) store.Reservation {
		res := store.Reservation{
			ID: store.NewReservationID(), RunID: r.ID, LeaseID: l.ID,
			Amount: money.MustParseUSD(amount), Status: store.ReservationPending,
			ExpiresAt: now.Add(expiresIn), CreatedAt: now,
		}
		if err := s.CreateReservation(ctx, res); err != nil {
			t.Fatalf("CreateReservation: %v", err)
		}
		return res
	}

	stale1 := mk("1.00", -time.Minute)
	stale2 := mk("2.00", -time.Second)
	fresh := mk("3.00", time.Hour)

	n, err := s.ExpirePendingReservations(ctx, now)
	if err != nil {
		t.Fatalf("ExpirePendingReservations: %v", err)
	}
	if n != 2 {
		t.Errorf("expired %d reservations, want 2", n)
	}

	for _, id := range []string{stale1.ID, stale2.ID} {
		got, _ := s.GetReservation(ctx, id)
		if got.Status != store.ReservationExpired {
			t.Errorf("reservation %s status = %q, want expired", id, got.Status)
		}
	}

	got, _ := s.GetReservation(ctx, fresh.ID)
	if got.Status != store.ReservationPending {
		t.Errorf("unexpired reservation status = %q, want pending", got.Status)
	}

	// Only the still-live hold counts against the budget now.
	total, _ := s.PendingReservationTotal(ctx, r.ID)
	if total != money.MustParseUSD("3.00") {
		t.Errorf("pending total = %s, want 3.00", total)
	}

	// Running the sweeper again reclaims nothing.
	if n, _ := s.ExpirePendingReservations(ctx, now); n != 0 {
		t.Errorf("second sweep expired %d, want 0", n)
	}
}

func TestPendingReservationTotalIsZeroWhenEmpty(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))

	total, err := s.PendingReservationTotal(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("PendingReservationTotal: %v", err)
	}
	if !total.IsZero() {
		t.Errorf("total = %s, want 0", total)
	}
}

func pendingReservation(runID, amount string) store.Reservation {
	now := time.Now()
	return store.Reservation{
		ID: store.NewReservationID(), RunID: runID,
		Amount: money.MustParseUSD(amount), Status: store.ReservationPending,
		ExpiresAt: now.Add(time.Minute), CreatedAt: now,
	}
}

func TestTryReserveAllowsExactExhaustionAndRejectsTheNextRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "exact-budget")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))
	appendEntry(t, s, p, r, "0.40")
	if err := s.CreateReservation(ctx, pendingReservation(r.ID, "0.20")); err != nil {
		t.Fatalf("seed hold: %v", err)
	}

	exact := pendingReservation(r.ID, "0.40")
	d, err := s.TryReserve(ctx, exact, true)
	if err != nil {
		t.Fatalf("exact reserve: %v", err)
	}
	if !d.Allowed || d.WouldBlock {
		t.Fatalf("exact reserve decision = %+v, want allowed", d)
	}

	next := pendingReservation(r.ID, "0.01")
	d, err = s.TryReserve(ctx, next, true)
	if err != nil {
		t.Fatalf("next reserve: %v", err)
	}
	if d.Allowed || !d.WouldBlock {
		t.Fatalf("next reserve decision = %+v, want blocked", d)
	}
	if d.Remaining != 0 || d.Shortfall != money.MustParseUSD("0.01") {
		t.Errorf("remaining/shortfall = %s/%s, want 0.00/0.01", d.Remaining, d.Shortfall)
	}
	if _, err := s.GetReservation(ctx, next.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("blocked reservation was persisted: %v", err)
	}
}

func TestTryReserveObserveModeRecordsWhatWouldBlock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "observe-budget")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("0.10"))
	res := pendingReservation(r.ID, "0.25")

	d, err := s.TryReserve(ctx, res, false)
	if err != nil {
		t.Fatalf("TryReserve: %v", err)
	}
	if !d.Allowed || !d.WouldBlock {
		t.Fatalf("decision = %+v, want allowed and would-block", d)
	}
	if got, err := s.GetReservation(ctx, res.ID); err != nil || got.Status != store.ReservationPending {
		t.Fatalf("observe reservation = (%+v, %v), want pending", got, err)
	}
}

func TestTryReserveCountsDescendantsAgainstParent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "parent-budget")
	parent := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))
	childA := seedRun(t, s, p.ID, parent.ID, money.MustParseUSD("1.00"))
	childB := seedRun(t, s, p.ID, parent.ID, money.MustParseUSD("1.00"))
	if err := s.CreateReservation(ctx, pendingReservation(childA.ID, "0.75")); err != nil {
		t.Fatalf("seed sibling hold: %v", err)
	}

	d, err := s.TryReserve(ctx, pendingReservation(childB.ID, "0.50"), true)
	if err != nil {
		t.Fatalf("TryReserve: %v", err)
	}
	if d.Allowed || d.RunID != parent.ID {
		t.Fatalf("decision = %+v, want parent %s to block", d, parent.ID)
	}
	if d.Held != money.MustParseUSD("0.75") || d.Shortfall != money.MustParseUSD("0.25") {
		t.Errorf("held/shortfall = %s/%s, want 0.75/0.25", d.Held, d.Shortfall)
	}
}

func TestConcurrentReservationsCannotOversubscribeRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "concurrent-budget")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))

	const requests = 20
	results := make(chan bool, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.TryReserve(ctx, pendingReservation(r.ID, "0.10"), true)
			if err != nil {
				errs <- err
				return
			}
			results <- d.Allowed
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("TryReserve: %v", err)
	}

	allowed := 0
	for ok := range results {
		if ok {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("allowed %d concurrent reservations, want exactly 10", allowed)
	}
	if total, err := s.PendingReservationTotal(ctx, r.ID); err != nil || total != money.MustParseUSD("1.00") {
		t.Errorf("pending total = (%s, %v), want 1.00", total, err)
	}
}

func TestSettleReservationIsAtomicAndIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "settlement")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))
	res := pendingReservation(r.ID, "0.50")
	if _, err := s.TryReserve(ctx, res, true); err != nil {
		t.Fatalf("TryReserve: %v", err)
	}
	entry := ledger.Entry{
		RunID: r.ID, PrincipalID: p.ID, Provider: "openai", Model: "gpt-4o",
		InputTokens: 10, OutputTokens: 20, Cost: money.MustParseUSD("0.12"),
	}

	first, err := s.SettleReservation(ctx, res.ID, entry)
	if err != nil {
		t.Fatalf("first settle: %v", err)
	}
	second, err := s.SettleReservation(ctx, res.ID, entry)
	if err != nil {
		t.Fatalf("idempotent settle: %v", err)
	}
	if second.Seq != first.Seq || second.Hash != first.Hash {
		t.Errorf("second settlement created a different entry: first=%+v second=%+v", first, second)
	}
	entries, _ := s.LedgerEntries(ctx, store.LedgerFilter{})
	if len(entries) != 1 {
		t.Errorf("ledger has %d entries, want 1", len(entries))
	}
	got, _ := s.GetReservation(ctx, res.ID)
	if got.Status != store.ReservationSettled || got.ResolvedAt == nil {
		t.Errorf("reservation after settle = %+v", got)
	}
}

func TestExpiredReservationCanStillRecordLateUsageOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "late-settlement")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))
	res := pendingReservation(r.ID, "0.50")
	res.ExpiresAt = time.Now().Add(-time.Second)
	if err := s.CreateReservation(ctx, res); err != nil {
		t.Fatalf("CreateReservation: %v", err)
	}
	if _, err := s.ExpirePendingReservations(ctx, time.Now()); err != nil {
		t.Fatalf("expire: %v", err)
	}

	entry := ledger.Entry{
		RunID: r.ID, PrincipalID: p.ID, Provider: "openai", Model: "gpt-4o",
		InputTokens: 10, OutputTokens: 20, Cost: money.MustParseUSD("0.12"),
	}
	first, err := s.SettleReservation(ctx, res.ID, entry)
	if err != nil {
		t.Fatalf("late settle: %v", err)
	}
	second, err := s.SettleReservation(ctx, res.ID, entry)
	if err != nil || second.Seq != first.Seq {
		t.Fatalf("late settlement retry = (%+v, %v), want seq %d", second, err, first.Seq)
	}
	got, _ := s.GetReservation(ctx, res.ID)
	if got.Status != store.ReservationExpired {
		t.Errorf("late settlement changed expired status to %q", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Ledger
// ---------------------------------------------------------------------------

func appendEntry(t *testing.T, s *Store, p store.Principal, r store.Run, cost string) ledger.Entry {
	t.Helper()

	e, err := s.AppendLedger(context.Background(), ledger.Entry{
		RunID: r.ID, PrincipalID: p.ID,
		Provider: "openai", Model: "gpt-4o",
		InputTokens: 1000, OutputTokens: 500,
		Cost:      money.MustParseUSD(cost),
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}
	return e
}

func TestLedgerAppendBuildsAChain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("100.00"))

	first := appendEntry(t, s, p, r, "0.01")
	if first.Seq != 1 {
		t.Errorf("first entry seq = %d, want 1", first.Seq)
	}
	if first.PrevHash != ledger.GenesisHash {
		t.Errorf("first entry PrevHash = %q, want the genesis hash", first.PrevHash)
	}

	second := appendEntry(t, s, p, r, "0.02")
	if second.Seq != 2 {
		t.Errorf("second entry seq = %d, want 2", second.Seq)
	}
	if second.PrevHash != first.Hash {
		t.Error("second entry does not chain onto the first")
	}

	entries, err := s.LedgerEntries(ctx, store.LedgerFilter{})
	if err != nil {
		t.Fatalf("LedgerEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if err := ledger.VerifyChain(entries); err != nil {
		t.Errorf("chain read back from the database does not verify: %v", err)
	}

	head, ok, err := s.LedgerHead(ctx)
	if err != nil || !ok {
		t.Fatalf("LedgerHead = (%v, %v, %v)", head, ok, err)
	}
	if head.Seq != 2 {
		t.Errorf("head seq = %d, want 2", head.Seq)
	}
}

func TestLedgerHeadOnEmptyLedger(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	_, ok, err := s.LedgerHead(context.Background())
	if err != nil {
		t.Fatalf("LedgerHead: %v", err)
	}
	if ok {
		t.Error("LedgerHead reported an entry in an empty ledger")
	}
}

// TestLedgerRejectsUpdate is the headline guarantee of this phase. The claim
// is that spend history cannot be rewritten through ANY path, so this test
// deliberately bypasses the store and issues raw SQL, exactly as a bug, a
// migration, or an operator at a prompt would.
func TestLedgerRejectsUpdate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("100.00"))
	appendEntry(t, s, p, r, "5.00")

	updates := []struct {
		name string
		sql  string
	}{
		{"change a cost", `UPDATE ledger SET cost_nanos = 1 WHERE seq = 1`},
		{"change a model", `UPDATE ledger SET model = 'gpt-3.5' WHERE seq = 1`},
		{"repair a hash", `UPDATE ledger SET hash = 'forged' WHERE seq = 1`},
		{"reassign a run", `UPDATE ledger SET run_id = 'run_other' WHERE seq = 1`},
		{"blanket update", `UPDATE ledger SET cost_nanos = 0`},
	}

	for _, tt := range updates {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.DB().ExecContext(ctx, tt.sql)
			if err == nil {
				t.Fatal("the database accepted an UPDATE against the ledger")
			}
			if !strings.Contains(err.Error(), "append-only") {
				t.Errorf("error %q is not the append-only trigger", err)
			}
		})
	}

	// The entry is untouched and the chain still verifies.
	entries, _ := s.LedgerEntries(ctx, store.LedgerFilter{})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Cost != money.MustParseUSD("5.00") {
		t.Errorf("cost = %s, want 5.00", entries[0].Cost)
	}
	if err := ledger.VerifyChain(entries); err != nil {
		t.Errorf("chain broken after rejected updates: %v", err)
	}
}

// TestLedgerRejectsDelete is the other half: history cannot be removed either.
func TestLedgerRejectsDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("100.00"))
	appendEntry(t, s, p, r, "1.00")
	appendEntry(t, s, p, r, "2.00")

	deletes := []struct {
		name string
		sql  string
	}{
		{"one row", `DELETE FROM ledger WHERE seq = 1`},
		{"every row", `DELETE FROM ledger`},
	}

	for _, tt := range deletes {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.DB().ExecContext(ctx, tt.sql)
			if err == nil {
				t.Fatal("the database accepted a DELETE against the ledger")
			}
			if !strings.Contains(err.Error(), "append-only") {
				t.Errorf("error %q is not the append-only trigger", err)
			}
		})
	}

	entries, _ := s.LedgerEntries(ctx, store.LedgerFilter{})
	if len(entries) != 2 {
		t.Errorf("got %d entries after rejected deletes, want 2", len(entries))
	}
}

// TestLedgerImmutableThroughStoreError checks that the trigger failure surfaces
// as ErrImmutable rather than an opaque driver error, so callers can tell an
// integrity refusal from a transient fault.
func TestLedgerImmutableIsMappedToSentinel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))
	appendEntry(t, s, p, r, "1.00")

	_, rawErr := s.DB().ExecContext(ctx, `DELETE FROM ledger WHERE seq = 1`)
	if rawErr == nil {
		t.Fatal("expected the trigger to reject the delete")
	}
	if mapped := wrap(rawErr, "deleting"); !errors.Is(mapped, store.ErrImmutable) {
		t.Errorf("wrap(%v) = %v, want ErrImmutable", rawErr, mapped)
	}
}

func TestLedgerFilters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)

	alice := seedPrincipal(t, s, "alice")
	bob := seedPrincipal(t, s, "bob")
	runA := seedRun(t, s, alice.ID, "", money.MustParseUSD("100.00"))
	runB := seedRun(t, s, alice.ID, "", money.MustParseUSD("100.00"))
	runC := seedRun(t, s, bob.ID, "", money.MustParseUSD("100.00"))

	appendEntry(t, s, alice, runA, "1.00")
	appendEntry(t, s, alice, runA, "2.00")
	appendEntry(t, s, alice, runB, "4.00")
	appendEntry(t, s, bob, runC, "8.00")

	tests := []struct {
		name   string
		filter store.LedgerFilter
		want   int
	}{
		{"no filter returns everything", store.LedgerFilter{}, 4},
		{"by run", store.LedgerFilter{RunID: runA.ID}, 2},
		{"by principal", store.LedgerFilter{PrincipalID: alice.ID}, 3},
		{"by run and principal", store.LedgerFilter{RunID: runB.ID, PrincipalID: alice.ID}, 1},
		{"limit caps results", store.LedgerFilter{Limit: 2}, 2},
		{"unmatched run returns none", store.LedgerFilter{RunID: "run_ghost"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.LedgerEntries(ctx, tt.filter)
			if err != nil {
				t.Fatalf("LedgerEntries: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("got %d entries, want %d", len(got), tt.want)
			}
		})
	}

	t.Run("spend rolls up per run and principal", func(t *testing.T) {
		if got, _ := s.SpendByRun(ctx, runA.ID); got != money.MustParseUSD("3.00") {
			t.Errorf("SpendByRun(runA) = %s, want 3.00", got)
		}
		if got, _ := s.SpendByPrincipal(ctx, alice.ID); got != money.MustParseUSD("7.00") {
			t.Errorf("SpendByPrincipal(alice) = %s, want 7.00", got)
		}
		if got, _ := s.SpendByPrincipal(ctx, bob.ID); got != money.MustParseUSD("8.00") {
			t.Errorf("SpendByPrincipal(bob) = %s, want 8.00", got)
		}
		if got, _ := s.SpendByRun(ctx, "run_ghost"); !got.IsZero() {
			t.Errorf("SpendByRun(missing) = %s, want 0", got)
		}
	})
}

// TestConcurrentLedgerAppends is the race the mutex exists for. Under -race,
// concurrent appends must produce a contiguous, verifiable chain rather than
// two entries claiming the same predecessor.
func TestConcurrentLedgerAppends(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "busy-agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("1000.00"))

	const goroutines, perGoroutine = 8, 10
	total := goroutines * perGoroutine

	var wg sync.WaitGroup
	errCh := make(chan error, total)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_, err := s.AppendLedger(ctx, ledger.Entry{
					RunID: r.ID, PrincipalID: p.ID,
					Provider: "openai", Model: fmt.Sprintf("model-%d-%d", g, i),
					InputTokens: 10, OutputTokens: 5,
					Cost:      money.MustParseUSD("0.001"),
					CreatedAt: time.Now(),
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent AppendLedger: %v", err)
	}

	entries, err := s.LedgerEntries(ctx, store.LedgerFilter{})
	if err != nil {
		t.Fatalf("LedgerEntries: %v", err)
	}
	if len(entries) != total {
		t.Fatalf("got %d entries, want %d", len(entries), total)
	}

	// Sequence numbers must be contiguous from 1, with no gaps or repeats.
	for i, e := range entries {
		if want := int64(i + 1); e.Seq != want {
			t.Fatalf("entry %d has seq %d, want %d", i, e.Seq, want)
		}
	}

	if err := ledger.VerifyChain(entries); err != nil {
		t.Errorf("concurrent appends produced a broken chain: %v", err)
	}

	want := money.MustParseUSD("0.001") * money.Nanos(total)
	if got, _ := s.SpendByRun(ctx, r.ID); got != want {
		t.Errorf("total spend = %s, want %s", got, want)
	}
}

// TestAppendLedgerDefaultsCreatedAt checks that an entry without an explicit
// timestamp still gets one, since the hash covers it.
func TestAppendLedgerDefaultsCreatedAt(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))

	e, err := s.AppendLedger(context.Background(), ledger.Entry{
		RunID: r.ID, PrincipalID: p.ID, Provider: "openai", Model: "gpt-4o",
		Cost: money.MustParseUSD("0.01"),
	})
	if err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}
	if e.CreatedAt.IsZero() {
		t.Error("CreatedAt was left zero")
	}
	if e.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", e.CreatedAt.Location())
	}
}

// TestEstimatedFlagSurvivesRoundTrip guards the rule that an unknown model
// must never look like a confidently priced one.
func TestEstimatedFlagSurvivesRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("10.00"))

	if _, err := s.AppendLedger(ctx, ledger.Entry{
		RunID: r.ID, PrincipalID: p.ID, Provider: "openai", Model: "some-new-model",
		Cost: money.MustParseUSD("0.05"), Estimated: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}

	entries, _ := s.LedgerEntries(ctx, store.LedgerFilter{})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if !entries[0].Estimated {
		t.Error("Estimated was lost on the round trip")
	}
	if err := ledger.VerifyChain(entries); err != nil {
		t.Errorf("chain does not verify: %v", err)
	}
}

func TestBudgetStatusUsesTheTightestAncestor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "budget-agent")
	parent := seedRun(t, s, p.ID, "", money.MustParseUSD("0.50"))
	child := seedRun(t, s, p.ID, parent.ID, money.MustParseUSD("1.00"))
	if _, err := s.AppendLedger(ctx, ledger.Entry{
		RunID: child.ID, PrincipalID: p.ID, Provider: "openai", Model: "gpt-4o-mini",
		Cost: money.MustParseUSD("0.20"), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateReservation(ctx, store.Reservation{
		ID: store.NewReservationID(), RunID: child.ID, Amount: money.MustParseUSD("0.10"),
		Status: store.ReservationPending, ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	status, err := s.BudgetStatus(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.SpendAllowed || status.Unlimited || status.LimitingRunID != parent.ID || status.EffectiveRemaining != money.MustParseUSD("0.20") {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Levels) != 2 || status.Levels[0].Remaining != money.MustParseUSD("0.70") ||
		status.Levels[1].Remaining != money.MustParseUSD("0.20") {
		t.Fatalf("levels = %+v", status.Levels)
	}
	if err := s.CloseRun(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	status, err = s.BudgetStatus(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.SpendAllowed || status.BlockingRunID != parent.ID || status.Levels[1].Status != store.RunClosed {
		t.Fatalf("closed-ancestor status = %+v", status)
	}
}
