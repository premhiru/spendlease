package dashboard

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

// fakeStore serves fixed summaries and records mode changes.
type fakeStore struct {
	summaries []store.PrincipalSummary
	err       error

	setID   string
	setMode store.Mode
	setErr  error
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
	t.Helper()

	d, err := New(Options{
		Store:   st,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "v-test",
		Models:  26,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mux := http.NewServeMux()
	d.Routes(mux)
	return mux
}

// get issues a request as if from the local machine.
//
// httptest.NewRequest uses a TEST-NET address, which the guard correctly
// treats as remote, so tests about rendering have to say where they are
// coming from.
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:54321"

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

// TestRefreshPausesWhileAButtonIsFocused documents the other half of that
// browser finding: a table that replaces itself every few seconds pulls the
// mode toggle out from under whoever is reaching for it.
func TestRefreshPausesWhileAButtonIsFocused(t *testing.T) {
	t.Parallel()

	st := &fakeStore{summaries: []store.PrincipalSummary{
		principal("prn_a", "agent", store.ModeObserve, "1.00", 1, 1, 0, 0),
	}}
	body := get(t, newTestDashboard(t, st), "/table").Body.String()

	if !strings.Contains(body, "hx-trigger") {
		t.Fatal("the table does not refresh at all")
	}
	if !strings.Contains(body, "activeElement") {
		t.Error("the refresh has no guard, so it will swap the table while a button is being clicked")
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
