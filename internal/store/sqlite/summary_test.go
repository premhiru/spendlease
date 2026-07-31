package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

// charge appends a ledger entry for a run.
func charge(t *testing.T, s *Store, p store.Principal, r store.Run, cost string, estimated bool) {
	t.Helper()

	if _, err := s.AppendLedger(context.Background(), ledger.Entry{
		RunID: r.ID, PrincipalID: p.ID,
		Provider: "openai", Model: "gpt-4o",
		InputTokens: 100, OutputTokens: 100,
		Cost:      money.MustParseUSD(cost),
		Estimated: estimated,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}
}

func TestPrincipalSummaries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)

	// Deliberately created cheapest-first, so a summary that simply returns
	// insertion order fails.
	cheap := seedPrincipal(t, s, "cheap-agent")
	expensive := seedPrincipal(t, s, "expensive-agent")
	idle := seedPrincipal(t, s, "idle-agent")

	cheapRun := seedRun(t, s, cheap.ID, "", money.MustParseUSD("10.00"))
	expRunA := seedRun(t, s, expensive.ID, "", money.MustParseUSD("1.00"))
	expRunB := seedRun(t, s, expensive.ID, "", money.MustParseUSD("100.00"))

	charge(t, s, cheap, cheapRun, "0.50", false)
	charge(t, s, expensive, expRunA, "5.00", false) // over its 1.00 budget
	charge(t, s, expensive, expRunB, "20.00", true)

	got, err := s.PrincipalSummaries(ctx)
	if err != nil {
		t.Fatalf("PrincipalSummaries: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d summaries, want 3", len(got))
	}

	t.Run("sorted by spend descending", func(t *testing.T) {
		want := []string{expensive.ID, cheap.ID, idle.ID}
		for i, id := range want {
			if got[i].ID != id {
				t.Errorf("row %d = %s, want %s", i, got[i].Name, id)
			}
		}
	})

	t.Run("totals are summed across runs", func(t *testing.T) {
		if want := money.MustParseUSD("25.00"); got[0].Spend != want {
			t.Errorf("expensive-agent spend = %s, want %s", got[0].Spend, want)
		}
		if got[0].Runs != 2 {
			t.Errorf("expensive-agent runs = %d, want 2", got[0].Runs)
		}
		if got[0].Entries != 2 {
			t.Errorf("expensive-agent entries = %d, want 2", got[0].Entries)
		}
	})

	t.Run("estimated entries are counted", func(t *testing.T) {
		if got[0].EstimatedEntries != 1 {
			t.Errorf("estimated = %d, want 1", got[0].EstimatedEntries)
		}
		if got[1].EstimatedEntries != 0 {
			t.Errorf("cheap-agent estimated = %d, want 0", got[1].EstimatedEntries)
		}
	})

	t.Run("over-budget runs are counted", func(t *testing.T) {
		// expRunA spent 5.00 against a 1.00 budget; expRunB spent 20.00
		// against 100.00 and is fine.
		if got[0].OverBudgetRuns != 1 {
			t.Errorf("expensive-agent over-budget runs = %d, want 1", got[0].OverBudgetRuns)
		}
		if got[1].OverBudgetRuns != 0 {
			t.Errorf("cheap-agent over-budget runs = %d, want 0", got[1].OverBudgetRuns)
		}
	})

	t.Run("a principal that has never spent still appears", func(t *testing.T) {
		last := got[2]
		if last.ID != idle.ID {
			t.Fatalf("last row = %s, want the idle agent", last.Name)
		}
		if !last.Spend.IsZero() || last.Entries != 0 || last.Runs != 0 {
			t.Errorf("idle agent = %+v, want all zeroes", last)
		}
		if last.LastActivity != nil {
			t.Error("an agent that never spent has a last-activity time")
		}
	})

	t.Run("activity time is recorded", func(t *testing.T) {
		if got[0].LastActivity == nil {
			t.Fatal("an agent that spent has no last-activity time")
		}
		if time.Since(*got[0].LastActivity) > time.Minute {
			t.Errorf("last activity = %v, want roughly now", got[0].LastActivity)
		}
	})
}

// TestPrincipalSummariesOnEmptyStore covers the first-run path.
func TestPrincipalSummariesOnEmptyStore(t *testing.T) {
	t.Parallel()

	got, err := newTestStore(t).PrincipalSummaries(context.Background())
	if err != nil {
		t.Fatalf("PrincipalSummaries: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d summaries from an empty store, want none", len(got))
	}
}

// TestZeroBudgetIsNeverOverBudget: a budget of zero means unset, not "no
// allowance". Treating it as a limit would mark every implicit run as
// over budget the moment it spent anything.
func TestZeroBudgetIsNeverOverBudget(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	p := seedPrincipal(t, s, "agent")
	r := seedRun(t, s, p.ID, "", 0)
	charge(t, s, p, r, "5.00", false)

	got, err := s.PrincipalSummaries(context.Background())
	if err != nil {
		t.Fatalf("PrincipalSummaries: %v", err)
	}
	if got[0].OverBudgetRuns != 0 {
		t.Errorf("over-budget runs = %d for a zero budget, want 0", got[0].OverBudgetRuns)
	}
}

func TestOperationalSummaryAndEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)
	p := seedPrincipal(t, s, "operational-agent")
	r := seedRun(t, s, p.ID, "", money.MustParseUSD("2.00"))

	seedLease(t, s, r.ID, time.Hour)
	seedLease(t, s, r.ID, -time.Hour)
	revoked := seedLease(t, s, r.ID, time.Hour)
	if err := s.RevokeLease(ctx, revoked.ID, time.Now()); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	charge(t, s, p, r, "0.25", false)

	for _, enforced := range []bool{true, false} {
		if err := s.RecordBudgetEvent(ctx, store.BudgetEvent{
			PrincipalID: p.ID,
			RunID:       r.ID,
			Provider:    "openai",
			Model:       "gpt-5.4-mini",
			Enforced:    enforced,
			Requested:   money.MustParseUSD("0.50"),
			Remaining:   money.MustParseUSD("0.10"),
			Shortfall:   money.MustParseUSD("0.40"),
			CreatedAt:   time.Now(),
		}); err != nil {
			t.Fatalf("RecordBudgetEvent: %v", err)
		}
	}

	summaries, err := s.PrincipalSummaries(ctx)
	if err != nil {
		t.Fatalf("PrincipalSummaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}
	sum := summaries[0]
	if sum.ActiveLeases != 1 || sum.RevokedLeases != 1 || sum.ExpiredLeases != 1 {
		t.Errorf("lease counts = active %d, revoked %d, expired %d",
			sum.ActiveLeases, sum.RevokedLeases, sum.ExpiredLeases)
	}
	if sum.BudgetBlocks != 1 || sum.WouldBlockEvents != 1 {
		t.Errorf("budget counts = blocked %d, observed %d", sum.BudgetBlocks, sum.WouldBlockEvents)
	}
	if sum.LastEvent == nil {
		t.Error("summary has no last operational event")
	}

	events, err := s.RecentOperationalEvents(ctx, store.OperationalEventFilter{Limit: 20}, time.Now())
	if err != nil {
		t.Fatalf("RecentOperationalEvents: %v", err)
	}
	wantKinds := map[store.OperationalEventKind]bool{
		store.EventAllowed:          false,
		store.EventBudgetBlocked:    false,
		store.EventBudgetWouldBlock: false,
		store.EventLeaseRevoked:     false,
		store.EventLeaseExpired:     false,
	}
	for _, event := range events {
		if _, ok := wantKinds[event.Kind]; ok {
			wantKinds[event.Kind] = true
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("recent events do not include %s: %+v", kind, events)
		}
	}

	filtered, err := s.RecentOperationalEvents(ctx, store.OperationalEventFilter{
		PrincipalID: p.ID,
		Kinds:       []store.OperationalEventKind{store.EventBudgetBlocked},
		Query:       r.ID,
		Since:       time.Now().Add(-time.Hour),
		Limit:       10,
	}, time.Now())
	if err != nil {
		t.Fatalf("filtered RecentOperationalEvents: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Kind != store.EventBudgetBlocked {
		t.Errorf("filtered events = %+v, want one budget block", filtered)
	}

	filtered, err = s.RecentOperationalEvents(ctx, store.OperationalEventFilter{
		Query: revoked.ID,
		Limit: 10,
	}, time.Now())
	if err != nil {
		t.Fatalf("lease search: %v", err)
	}
	if len(filtered) != 1 || filtered[0].LeaseID != revoked.ID {
		t.Errorf("lease search = %+v, want %s", filtered, revoked.ID)
	}

	filtered, err = s.RecentOperationalEvents(ctx, store.OperationalEventFilter{
		PrincipalID: "prn_missing",
		Limit:       10,
	}, time.Now())
	if err != nil {
		t.Fatalf("principal filter: %v", err)
	}
	if len(filtered) != 0 {
		t.Errorf("missing principal returned events: %+v", filtered)
	}
}

func TestRunSummaries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)

	p := seedPrincipal(t, s, "agent")
	other := seedPrincipal(t, s, "somebody-else")

	small := seedRun(t, s, p.ID, "", money.MustParseUSD("1.00"))
	large := seedRun(t, s, p.ID, "", money.MustParseUSD("50.00"))
	child := seedRun(t, s, p.ID, large.ID, money.MustParseUSD("5.00"))
	otherRun := seedRun(t, s, other.ID, "", money.MustParseUSD("10.00"))

	charge(t, s, p, small, "2.00", false) // over its 1.00 budget
	charge(t, s, p, large, "30.00", false)
	charge(t, s, p, child, "1.00", false)
	charge(t, s, other, otherRun, "99.00", false)

	got, err := s.RunSummaries(ctx, p.ID)
	if err != nil {
		t.Fatalf("RunSummaries: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d runs, want 3 (another principal's must not appear)", len(got))
	}

	if got[0].ID != large.ID {
		t.Errorf("first run = %s, want the highest-spending one", got[0].ID)
	}
	if want := money.MustParseUSD("30.00"); got[0].Spend != want {
		t.Errorf("spend = %s, want %s", got[0].Spend, want)
	}

	byID := map[string]store.RunSummary{}
	for _, r := range got {
		byID[r.ID] = r
	}

	if !byID[small.ID].OverBudget() {
		t.Error("a run that spent 2.00 against a 1.00 budget is not marked over budget")
	}
	if byID[large.ID].OverBudget() {
		t.Error("a run within its budget is marked over budget")
	}
	if byID[child.ID].ParentRunID != large.ID {
		t.Errorf("child run parent = %q, want %s", byID[child.ID].ParentRunID, large.ID)
	}
}
