package dashboard

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

// fakeStore serves fixed summaries and records mode changes.
type fakeStore struct {
	summaries []store.PrincipalSummary
	events    []store.OperationalEvent
	err       error

	setID   string
	setMode store.Mode
	setErr  error

	eventFilterMu sync.Mutex
	eventFilter   store.OperationalEventFilter
}

func (f *fakeStore) RecentOperationalEvents(_ context.Context, filter store.OperationalEventFilter, _ time.Time) ([]store.OperationalEvent, error) {
	f.eventFilterMu.Lock()
	defer f.eventFilterMu.Unlock()
	f.eventFilter = filter
	return f.events, f.err
}

func (f *fakeStore) lastEventFilter() store.OperationalEventFilter {
	f.eventFilterMu.Lock()
	defer f.eventFilterMu.Unlock()
	return f.eventFilter
}

func (f *fakeStore) PrincipalSummaries(context.Context) ([]store.PrincipalSummary, error) {
	return f.summaries, f.err
}

func (f *fakeStore) SetPrincipalMode(_ context.Context, id string, m store.Mode) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.setID, f.setMode = id, m
	for i := range f.summaries {
		if f.summaries[i].ID == id {
			f.summaries[i].Mode = m
		}
	}
	return nil
}

func principal(id, name string, mode store.Mode, spend string, runs, entries, estimated, over int) store.PrincipalSummary {
	seen := time.Now().Add(-2 * time.Minute)
	return store.PrincipalSummary{
		Principal: store.Principal{
			ID: id, Name: name, Mode: mode, CreatedAt: time.Now().Add(-time.Hour),
		},
		Spend:            money.MustParseUSD(spend),
		Runs:             runs,
		Entries:          entries,
		EstimatedEntries: estimated,
		OverBudgetRuns:   over,
		LastActivity:     &seen,
	}
}

func newTestDashboard(t *testing.T, st *fakeStore) http.Handler {
	return newTestDashboardWithRevoker(t, st, nil)
}

func newTestDashboardWithRevoker(t *testing.T, st *fakeStore, revoker PrincipalRevoker) http.Handler {
	t.Helper()

	d, err := New(Options{
		Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "v-test",
		PricingRevision: "abcdef0123456789", PricingEffective: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		PricingLoadedAt:           time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC),
		PricingOldestVerified:     time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
		PricingVerificationMaxAge: 100 * 365 * 24 * time.Hour,
		PricingProviders:          2, PricingModels: 26, Revoker: revoker,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mux := http.NewServeMux()
	d.Routes(mux)
	return mux
}

type fakeRevoker struct{ count int }

func (f fakeRevoker) RevokePrincipal(context.Context, string) (int, error) { return f.count, nil }

// get issues a request as if from the local machine.
//
// httptest.NewRequest uses a TEST-NET address, which the guard correctly
// treats as remote, so tests about rendering have to say where they are
// coming from.
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "localhost:4000"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestTemplatesParse is the guard that makes a broken template a startup
// failure rather than a 500 somebody discovers later.
func TestTemplatesParse(t *testing.T) {
	t.Parallel()

	if _, err := New(Options{Store: &fakeStore{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatalf("the embedded templates do not parse: %v", err)
	}
}

// TestSortedBySpendDescending is the one thing the dashboard has to get right.
//
// During an incident the question is "which agent is costing me money", and
// the answer is the top row. If the order is wrong the page is worse than
// useless, because it points at the wrong agent.
func TestSortedBySpendDescending(t *testing.T) {
	t.Parallel()

	// Deliberately supplied in the order the store returns them, which is
	// already sorted; the view must not disturb it.
	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_big", "expensive-agent", store.ModeObserve, "128.50", 3, 900, 0, 1),
		principal("prn_mid", "middling-agent", store.ModeObserve, "12.25", 2, 80, 4, 0),
		principal("prn_low", "cheap-agent", store.ModeEnforce, "0.01", 1, 2, 0, 0),
		principal("prn_nil", "idle-agent", store.ModeObserve, "0.00", 0, 0, 0, 0),
	}}

	body := get(t, newTestDashboard(t, st), "/").Body.String()

	order := []string{"expensive-agent", "middling-agent", "cheap-agent", "idle-agent"}
	positions := make([]int, len(order))
	for i, name := range order {
		positions[i] = strings.Index(body, name)
		if positions[i] < 0 {
			t.Fatalf("%s is missing from the page", name)
		}
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] < positions[i-1] {
			t.Errorf("%s appears above %s; the table is not sorted by spend descending",
				order[i], order[i-1])
		}
	}
}

// TestNoCharts holds the line the brief draws. A chart of spend over time
// answers a question nobody asks during an incident.
func TestNoCharts(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeObserve, "1.00", 1, 1, 0, 0),
	}}
	body := strings.ToLower(get(t, newTestDashboard(t, st), "/").Body.String())

	for _, forbidden := range []string{"<canvas", "chart.js", "d3.js", "plotly", "<svg"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the dashboard contains %q; it is one table, deliberately", forbidden)
		}
	}
}

func TestTotals(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "a", store.ModeObserve, "1.50", 1, 1, 0, 0),
		principal("prn_b", "b", store.ModeObserve, "2.25", 1, 1, 0, 0),
		principal("prn_c", "c", store.ModeObserve, "0.0025", 1, 1, 0, 0),
	}}

	body := get(t, newTestDashboard(t, st), "/table").Body.String()
	if !strings.Contains(body, "3.7525") {
		t.Errorf("the total is missing or wrong; body:\n%s", body)
	}
}

// TestOverBudgetIsShown covers the signal that makes observe mode worth
// running: a request that was served and would not have been under
// enforcement.
func TestOverBudgetIsShown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		over int
		want bool
	}{
		{name: "a run over its budget is flagged", over: 1, want: true},
		{name: "several over budget still flags once", over: 5, want: true},
		{name: "within budget is not flagged", over: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st := &fakeStore{summaries: []store.PrincipalSummary{
				principal("prn_a", "agent", store.ModeObserve, "5.00", 1, 10, 0, tt.over),
			}}
			body := get(t, newTestDashboard(t, st), "/table").Body.String()

			got := strings.Contains(body, "would have been blocked")
			if got != tt.want {
				t.Errorf("over-budget flag shown = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEstimatedCountIsShown checks that a total built partly from guesses says
// so, rather than presenting an estimate as a fact.
func TestEstimatedCountIsShown(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeObserve, "5.00", 1, 10, 4, 0),
	}}
	body := get(t, newTestDashboard(t, st), "/table").Body.String()

	if !strings.Contains(body, "~4") {
		t.Error("the estimated-entry count is not shown")
	}
	if !strings.Contains(body, "estimated") {
		t.Error("nothing explains what the estimated count means")
	}
}

func TestOperationalStatusAndRecentEventsAreShown(t *testing.T) {
	t.Parallel()

	now := time.Now()
	active := principal("prn_active", "active-agent", store.ModeEnforce, "0.10", 1, 2, 0, 0)
	active.ActiveLeases = 2
	active.RevokedLeases = 1
	active.ExpiredLeases = 3
	active.BudgetBlocks = 4
	active.LastEvent = &now
	revoked := principal("prn_revoked", "revoked-agent", store.ModeEnforce, "0.20", 1, 1, 0, 0)
	revoked.RevokedLeases = 2
	revoked.LastEvent = &now

	st := &fakeStore{
		summaries: []store.PrincipalSummary{active, revoked},
		events: []store.OperationalEvent{
			{Kind: store.EventBudgetBlocked, PrincipalName: "active-agent", RunID: "run_blocked", Amount: money.MustParseUSD("0.50"), Remaining: money.MustParseUSD("0.10"), CreatedAt: now},
			{Kind: store.EventLeaseRevoked, PrincipalName: "revoked-agent", RunID: "run_revoked", LeaseID: "lse_revoked", CreatedAt: now},
		},
	}
	body := get(t, newTestDashboard(t, st), "/table").Body.String()
	for _, want := range []string{
		"Active", "2 active · 1 revoked · 3 expired", "Budget blocked",
		"needed $0.50; $0.10 remaining", "Revoked", "No active leases",
		"Recent events", "lse_revoked",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
}

func TestDashboardExplainsBuildAndPriceBookFreshness(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeEnforce, "0.00", 1, 0, 0, 0),
	}}
	body := get(t, newTestDashboard(t, st), "/").Body.String()
	for _, want := range []string{
		"Build v-test",
		"Price book abcdef01 · rates through 31 Jul 2026",
		"Loaded 1 Aug 2026 12:30 UTC · 2 providers · 26 price entries",
		"Pricing verified 6 Aug 2026",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard header is missing %q", want)
		}
	}
	if got := dashboardBuildLabel("dev"); got != "Local development build" {
		t.Errorf("development build label = %q", got)
	}
}

func TestPriceBookFreshnessWarnsWhenEvidenceIsOldOrMissing(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	label, detail, class := priceBookFreshness(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), 0, 0, now, 45*24*time.Hour)
	if label != "Pricing verification stale" || !strings.Contains(detail, "pricing verify") || class != "freshness-warning" {
		t.Fatalf("stale presentation = %q, %q, %q", label, detail, class)
	}
	label, detail, class = priceBookFreshness(time.Time{}, 3, 0, now, 45*24*time.Hour)
	if label != "Pricing verification incomplete" || !strings.Contains(detail, "3 active price entries") || class != "freshness-warning" {
		t.Fatalf("missing presentation = %q, %q, %q", label, detail, class)
	}
	label, detail, class = priceBookFreshness(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), 0, 1, now, 45*24*time.Hour)
	if label != "Pricing verification invalid" || !strings.Contains(detail, "future vendor-review date") || class != "freshness-warning" {
		t.Fatalf("future presentation = %q, %q, %q", label, detail, class)
	}
}

func TestEventFiltersAreServerSideAndPreserved(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent-a", store.ModeEnforce, "0.00", 1, 0, 0, 0),
		principal("prn_b", "agent-b", store.ModeEnforce, "0.00", 1, 0, 0, 0),
	}}
	body := get(t, newTestDashboard(t, st),
		"/table?event_agent=prn_b&event_kind=allowed&event_since=7d&event_q=lse_target&event_limit=40").Body.String()

	filter := st.lastEventFilter()
	if filter.PrincipalID != "prn_b" || filter.Query != "lse_target" || filter.Limit != 41 {
		t.Errorf("store filter = %+v", filter)
	}
	if len(filter.Kinds) != 1 || filter.Kinds[0] != store.EventAllowed {
		t.Errorf("event kinds = %v, want allowed", filter.Kinds)
	}
	if age := time.Since(filter.Since); age < 6*24*time.Hour || age > 8*24*time.Hour {
		t.Errorf("since is not about seven days ago: %s", filter.Since)
	}
	for _, want := range []string{
		`value="prn_b" selected`,
		`value="allowed" selected`,
		`value="7d" selected`,
		`value="lse_target"`,
		`hx-include="#event-filters"`,
		"No events match these filters",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("filtered dashboard is missing %q", want)
		}
	}
}

func TestDefaultEventFilterPrioritisesAttention(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeEnforce, "0.00", 1, 0, 0, 0),
	}}
	body := get(t, newTestDashboard(t, st), "/table").Body.String()
	filter := st.lastEventFilter()
	if len(filter.Kinds) != len(attentionEventKinds) || filter.Limit != eventPageSize+1 {
		t.Errorf("default store filter = %+v", filter)
	}
	if filter.Since.IsZero() {
		t.Error("default event filter has no time window")
	}
	if !strings.Contains(body, `value="attention" selected`) || !strings.Contains(body, `value="24h" selected`) {
		t.Error("default attention and 24-hour choices are not selected")
	}
}

func TestEventLoadMoreRequestsTheNextPageSize(t *testing.T) {
	t.Parallel()

	now := time.Now()
	events := make([]store.OperationalEvent, eventPageSize+1)
	for i := range events {
		events[i] = store.OperationalEvent{
			Kind: store.EventBudgetBlocked, PrincipalName: "agent", RunID: fmt.Sprintf("run_%02d", i), CreatedAt: now,
		}
	}
	st := &fakeStore{
		summaries: []store.PrincipalSummary{principal("prn_a", "agent", store.ModeEnforce, "0.00", 1, 0, 0, 0)},
		events:    events,
	}
	body := get(t, newTestDashboard(t, st), "/table").Body.String()
	if !strings.Contains(body, "Load 20 more") || !strings.Contains(body, "event_limit=40") {
		t.Errorf("load-more control is missing or has the wrong limit")
	}
	if strings.Contains(body, "run_20") {
		t.Error("lookahead event was rendered instead of being reserved for load-more detection")
	}
}

func TestRevokeReturnsVisibleConfirmation(t *testing.T) {
	t.Parallel()

	summary := principal("prn_a", "agent", store.ModeEnforce, "0.00", 1, 0, 0, 0)
	summary.ActiveLeases = 2
	st := &fakeStore{summaries: []store.PrincipalSummary{summary}}
	h := newTestDashboardWithRevoker(t, st, fakeRevoker{count: 2})
	req := httptest.NewRequest(http.MethodPost, "/admin/principals/prn_a/revoke", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "localhost:4000"
	req.Header.Set(AdminRequestHeader, "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Revoked 2 active leases") || !strings.Contains(body, `role="status"`) {
		t.Errorf("revoke response has no visible confirmation: %s", body)
	}
}

// TestEmptyState covers the first thing a new user sees.
func TestEmptyState(t *testing.T) {
	t.Parallel()

	body := get(t, newTestDashboard(t, &fakeStore{}), "/").Body.String()

	if !strings.Contains(body, "No agents yet") {
		t.Error("the empty state does not say there are no agents")
	}
	// It has to say what to do next, or a new user is stuck on a blank page.
	if !strings.Contains(body, "keys principal create") {
		t.Error("the empty state does not give the command to create an agent")
	}
}

// TestModeToggle covers the single-click switch the design calls for.
func TestModeToggle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mode       string
		wantStatus int
		wantMode   store.Mode
	}{
		{name: "to enforce", mode: "enforce", wantStatus: http.StatusOK, wantMode: store.ModeEnforce},
		{name: "to observe", mode: "observe", wantStatus: http.StatusOK, wantMode: store.ModeObserve},
		{name: "an unknown mode is refused", mode: "paranoid", wantStatus: http.StatusBadRequest},
		{name: "an empty mode is refused", mode: "", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st := &fakeStore{summaries: []store.PrincipalSummary{
				principal("prn_a", "agent", store.ModeObserve, "1.00", 1, 1, 0, 0),
			}}
			h := newTestDashboard(t, st)

			form := url.Values{"mode": {tt.mode}}
			req := httptest.NewRequest(http.MethodPost, "/admin/principals/prn_a/mode",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.RemoteAddr = "127.0.0.1:54321"
			req.Host = "localhost:4000"
			req.Header.Set(AdminRequestHeader, "1")

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				if st.setID != "" {
					t.Error("an invalid mode still reached the store")
				}
				return
			}

			if st.setID != "prn_a" || st.setMode != tt.wantMode {
				t.Errorf("store received (%q, %q), want (prn_a, %q)", st.setID, st.setMode, tt.wantMode)
			}
			// The response is the refreshed table, so htmx can swap it in.
			if !strings.Contains(rec.Body.String(), `id="table"`) {
				t.Error("the toggle did not return the refreshed table")
			}
		})
	}
}

func TestDashboardNamesRuntimeEnforcementPolicy(t *testing.T) {
	t.Parallel()

	for _, policy := range []string{"strict", "best-effort"} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()
			st := &fakeStore{summaries: []store.PrincipalSummary{
				principal("prn_a", "agent", store.ModeEnforce, "1.00", 1, 1, 0, 0),
			}}
			d, err := New(Options{
				Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				EnforcementPolicy: policy,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			mux := http.NewServeMux()
			d.Routes(mux)
			body := get(t, mux, "/").Body.String()
			if !strings.Contains(body, policy+" enforcement") {
				t.Errorf("dashboard does not name %s policy", policy)
			}
			if strings.Contains(body, ">enforce</") {
				t.Error("dashboard exposes the ambiguous persisted mode instead of the runtime policy")
			}
		})
	}
}

// TestAgentNamesAreEscaped guards against a principal name becoming script.
//
// Names are chosen by whoever creates the principal, which in a multi-user
// deployment is not necessarily the person reading the dashboard.
func TestAgentNamesAreEscaped(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_x", `<script>alert("xss")</script>`, store.ModeObserve, "1.00", 1, 1, 0, 0),
	}}
	body := get(t, newTestDashboard(t, st), "/table").Body.String()

	if strings.Contains(body, "<script>alert") {
		t.Fatalf("an agent name was rendered as live markup:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("the name was not rendered at all; it should appear, escaped")
	}
}

// TestStoreFailureIsReported checks the page fails loudly rather than showing
// an empty table, which would read as "nothing is spending money".
func TestStoreFailureIsReported(t *testing.T) {
	t.Parallel()

	st := &fakeStore{err: context.DeadlineExceeded}
	h := newTestDashboard(t, st)

	for _, path := range []string{"/", "/table"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s returned %d on a store failure, want 500", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "No agents yet") {
			t.Errorf("%s showed the empty state on a store failure, which reads as 'nothing is spending'", path)
		}
	}
}

// TestTableIsNotCached: a stale dashboard during an incident is worse than a
// slow one.
func TestTableIsNotCached(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeObserve, "1.00", 1, 1, 0, 0),
	}}
	rec := get(t, newTestDashboard(t, st), "/table")

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// TestSingleTableElement is the regression test for a duplicate-id bug found
// by opening the page in a browser: the layout wrapped the table template in
// a second element that also carried id="table" and its own polling trigger,
// so the page had two nodes with the same id and refreshed twice as often.
func TestSingleTableElement(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeObserve, "1.00", 1, 1, 0, 0),
	}}

	for _, path := range []string{"/", "/table"} {
		body := get(t, newTestDashboard(t, st), path).Body.String()
		if n := strings.Count(body, `id="table"`); n != 1 {
			t.Errorf("%s contains %d elements with id=\"table\", want exactly 1", path, n)
		}
	}
}

// TestRefreshPausesWhileAControlIsFocused documents the other half of that
// browser finding: a table that replaces itself every few seconds pulls a
// mode toggle or filter input out from under the operator.
func TestRefreshPausesWhileAControlIsFocused(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeObserve, "1.00", 1, 1, 0, 0),
	}}
	body := get(t, newTestDashboard(t, st), "/table").Body.String()

	if !strings.Contains(body, "hx-trigger") {
		t.Fatal("the table does not refresh at all")
	}
	if !strings.Contains(body, "activeElement") {
		t.Error("the refresh has no guard, so it will swap the table while a control is in use")
	}
	for _, control := range []string{"button", "input", "select"} {
		if !strings.Contains(body, control) {
			t.Errorf("the refresh guard does not pause for %s", control)
		}
	}
}

func TestRelative(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	tests := []struct {
		name string
		in   *time.Time
		want string
	}{
		{name: "never", in: nil, want: "never"},
		{name: "seconds", in: ago(5 * time.Second), want: "just now"},
		{name: "one minute", in: ago(time.Minute), want: "1 minute ago"},
		{name: "minutes", in: ago(42 * time.Minute), want: "42 minutes ago"},
		{name: "one hour", in: ago(time.Hour), want: "1 hour ago"},
		{name: "hours", in: ago(5 * time.Hour), want: "5 hours ago"},
		{name: "one day", in: ago(25 * time.Hour), want: "1 day ago"},
		{name: "days", in: ago(72 * time.Hour), want: "3 days ago"},
		{name: "clock skew reads as now", in: ago(-time.Minute), want: "just now"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := relative(tt.in, now); got != tt.want {
				t.Errorf("relative() = %q, want %q", got, tt.want)
			}
		})
	}
}
