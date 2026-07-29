// Package dashboard renders the spend table.
//
// It is deliberately one table, sorted by spend descending, with no charts.
// The question an operator has during an incident is "which agent is costing
// me money right now", and that is answered by sorting rather than by
// plotting. A chart of spend over time answers a question nobody asks at 3am.
package dashboard

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/web"
)

// SummaryStore is the slice of the store the dashboard reads.
type SummaryStore interface {
	// PrincipalSummaries returns principals with their totals, ordered by
	// spend descending.
	PrincipalSummaries(ctx context.Context) ([]store.PrincipalSummary, error)
	// SetPrincipalMode switches a principal between observe and enforce.
	SetPrincipalMode(ctx context.Context, id string, m store.Mode) error
}

// PrincipalRevoker is the kill-switch surface used by the dashboard.
type PrincipalRevoker interface {
	RevokePrincipal(context.Context, string) (int, error)
}

// Options configures a dashboard.
type Options struct {
	// Store supplies the rows. Required.
	Store SummaryStore
	// Logger receives errors. Required.
	Logger *slog.Logger
	// Version is shown in the header.
	Version string
	// Models is how many models the price book knows, shown in the header.
	Models int
	// Warning, if set, is displayed above the table. It carries the reason a
	// deployment is not production-ready rather than leaving it implicit.
	Warning string
	// Guard controls access from outside the local machine.
	Guard Guard
	// Revoker activates the principal-wide kill switch.
	Revoker PrincipalRevoker
}

// Dashboard serves the spend table.
type Dashboard struct {
	store   SummaryStore
	logger  *slog.Logger
	tmpl    *template.Template
	static  http.Handler
	guard   Guard
	version string
	models  int
	warning string
	revoker PrincipalRevoker
}

// New parses the templates and returns a dashboard.
//
// Parsing at construction rather than per request means a broken template is
// a startup failure instead of a 500 discovered by a user.
func New(opts Options) (*Dashboard, error) {
	tmpl, err := template.ParseFS(web.Templates, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Dashboard{
		store:   opts.Store,
		logger:  opts.Logger,
		tmpl:    tmpl,
		static:  http.StripPrefix("/static/", http.FileServer(http.FS(web.Static()))),
		guard:   opts.Guard,
		version: opts.Version,
		models:  opts.Models,
		warning: opts.Warning,
		revoker: opts.Revoker,
	}, nil
}

// Routes registers the dashboard's handlers on a mux.
//
// Everything except the static assets is behind the guard: spend figures are
// as sensitive as the control that changes them, and a read-only view of
// which agent is burning money is not something to serve to the internet
// either.
func (d *Dashboard) Routes(mux *http.ServeMux) {
	mux.Handle("GET /{$}", d.guard.Protect(http.HandlerFunc(d.handlePage)))
	mux.Handle("GET /table", d.guard.Protect(http.HandlerFunc(d.handleTable)))
	mux.Handle("POST /admin/principals/{id}/mode", d.guard.Protect(http.HandlerFunc(d.handleSetMode)))
	if d.revoker != nil {
		mux.Handle("POST /admin/principals/{id}/revoke", d.guard.Protect(http.HandlerFunc(d.handleRevoke)))
	}

	// Stylesheet and htmx. No secrets, and requiring credentials for them
	// would mean an unauthenticated browser could not even render the login
	// prompt properly.
	mux.Handle("GET /static/", d.static)
}

func (d *Dashboard) handleRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := d.revoker.RevokePrincipal(r.Context(), id)
	if err != nil {
		d.logger.Error("revoking principal", "principal", id, "error", err)
		http.Error(w, "could not revoke principal", http.StatusInternalServerError)
		return
	}
	d.logger.Warn("principal kill switch activated", "principal", id, "leases", n)
	d.handleTable(w, r)
}

// view is what the templates render.
type view struct {
	Version    string
	Models     int
	Warning    string
	Principals []row
	Total      money.Nanos
}

// row is one principal in the table.
//
// It is a view type rather than the store's summary because the template
// should not be doing arithmetic or formatting decisions.
type row struct {
	ID               string
	Name             string
	Mode             store.Mode
	Runs             int
	Entries          int
	EstimatedEntries int
	Spend            money.Nanos
	LastSeen         string
	OverBudget       bool
}

// handlePage renders the whole page.
func (d *Dashboard) handlePage(w http.ResponseWriter, r *http.Request) {
	v, err := d.build(r.Context())
	if err != nil {
		d.fail(w, "building the dashboard", err)
		return
	}
	d.render(w, "dashboard.html", v)
}

// handleTable renders just the table, which is what htmx polls for.
func (d *Dashboard) handleTable(w http.ResponseWriter, r *http.Request) {
	v, err := d.build(r.Context())
	if err != nil {
		d.fail(w, "building the table", err)
		return
	}
	d.render(w, "table", v)
}

// handleSetMode switches a principal between observe and enforce and returns
// the refreshed table.
//
// This is the "single dashboard toggle" the design calls for. Switching modes
// should not require reading documentation or finding an API reference,
// because a principal left in observe mode out of inconvenience is a principal
// that never gets enforced.
func (d *Dashboard) handleSetMode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	mode := store.Mode(strings.TrimSpace(r.FormValue("mode")))
	if !mode.Valid() {
		http.Error(w, "mode must be observe or enforce", http.StatusBadRequest)
		return
	}

	if err := d.store.SetPrincipalMode(r.Context(), id, mode); err != nil {
		d.logger.Error("switching principal mode", "principal", id, "mode", mode, "error", err)
		http.Error(w, "could not switch mode", http.StatusInternalServerError)
		return
	}
	d.logger.Info("principal mode changed", "principal", id, "mode", mode)

	d.handleTable(w, r)
}

// build assembles the view.
func (d *Dashboard) build(ctx context.Context) (view, error) {
	summaries, err := d.store.PrincipalSummaries(ctx)
	if err != nil {
		return view{}, err
	}

	v := view{Version: d.version, Models: d.models, Warning: d.warning}
	now := time.Now()

	for _, s := range summaries {
		v.Total += s.Spend
		v.Principals = append(v.Principals, row{
			ID:               s.ID,
			Name:             s.Name,
			Mode:             s.Mode,
			Runs:             s.Runs,
			Entries:          s.Entries,
			EstimatedEntries: s.EstimatedEntries,
			Spend:            s.Spend,
			LastSeen:         relative(s.LastActivity, now),
			OverBudget:       s.OverBudgetRuns > 0,
		})
	}
	return v, nil
}

// render writes a template.
func (d *Dashboard) render(w http.ResponseWriter, name string, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The numbers change constantly and a stale dashboard during an incident
	// is worse than a slow one.
	w.Header().Set("Cache-Control", "no-store")

	if err := d.tmpl.ExecuteTemplate(w, name, v); err != nil {
		// The status is already sent by now, so this can only be logged.
		d.logger.Error("rendering the dashboard", "template", name, "error", err)
	}
}

// fail reports a failure to the operator and the log.
func (d *Dashboard) fail(w http.ResponseWriter, doing string, err error) {
	d.logger.Error(doing, "error", err)
	http.Error(w, "spendlease could not read the ledger", http.StatusInternalServerError)
}

// relative renders a timestamp as an age, which is what somebody watching a
// dashboard actually wants to know.
func relative(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}

	d := now.Sub(*t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

// plural renders "1 minute ago" and "3 minutes ago".
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return strconv.Itoa(n) + " " + unit + "s ago"
}
