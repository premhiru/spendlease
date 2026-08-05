// Package dashboard renders the spend summary and filtered operational event
// stream, with state-changing controls protected by Guard.
package dashboard

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/operator"
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
	// RecentOperationalEvents supplies the allowed, blocked and lease-lifecycle
	// timeline below the summary table.
	RecentOperationalEvents(ctx context.Context, filter store.OperationalEventFilter, now time.Time) ([]store.OperationalEvent, error)
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
	// PricingRevision identifies the exact active price-book contents.
	PricingRevision string
	// PricingEffective is the newest active rate date.
	PricingEffective time.Time
	// PricingLoadedAt is when the running process loaded those rates.
	PricingLoadedAt time.Time
	// PricingProviders and PricingModels add coverage context to the tooltip.
	PricingProviders int
	PricingModels    int
	// Warning, if set, is displayed above the table. It carries the reason a
	// deployment is not production-ready rather than leaving it implicit.
	Warning string
	// Guard controls access from outside the local machine.
	Guard Guard
	// Revoker activates the principal-wide kill switch.
	Revoker PrincipalRevoker
	// Manager enables guided principal, run and lease management. When nil,
	// the dashboard remains read-only apart from the existing mode and kill
	// controls.
	Manager AgentManager
	// LeaseRevoker revokes one lease from the management workspace.
	LeaseRevoker LeaseRevoker
	// Credentials enables the admin-only provider settings panel. Plaintext
	// keys are accepted for storage but are never returned by this interface.
	Credentials ProviderCredentials
	// CredentialStatus reports which providers have a key without granting
	// write access. The demo uses this to show its mock credential while
	// keeping the provider settings form disabled.
	CredentialStatus ProviderStatusSource
	// Providers is the set of names the running gateway actually routes.
	Providers []string
}

// Dashboard serves the spend table.
type Dashboard struct {
	store            SummaryStore
	logger           *slog.Logger
	tmpl             *template.Template
	static           http.Handler
	guard            Guard
	version          string
	pricingLabel     string
	pricingDetail    string
	warning          string
	revoker          PrincipalRevoker
	manager          AgentManager
	leaseRevoker     LeaseRevoker
	credentials      ProviderCredentials
	credentialStatus ProviderStatusSource
	providers        []string
	providerSet      map[string]struct{}
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
	credentialStatus := opts.CredentialStatus
	if credentialStatus == nil {
		credentialStatus, _ = opts.Credentials.(ProviderStatusSource)
	}
	d := &Dashboard{
		store:            opts.Store,
		logger:           opts.Logger,
		tmpl:             tmpl,
		static:           http.StripPrefix("/static/", http.FileServer(http.FS(web.Static()))),
		guard:            opts.Guard,
		version:          opts.Version,
		pricingLabel:     priceBookLabel(opts.PricingRevision, opts.PricingEffective),
		pricingDetail:    priceBookDetail(opts.PricingLoadedAt, opts.PricingProviders, opts.PricingModels),
		warning:          opts.Warning,
		revoker:          opts.Revoker,
		manager:          opts.Manager,
		leaseRevoker:     opts.LeaseRevoker,
		credentials:      opts.Credentials,
		credentialStatus: credentialStatus,
		providers:        normalizeProviderNames(opts.Providers),
		providerSet:      make(map[string]struct{}),
	}
	for _, name := range d.providers {
		d.providerSet[name] = struct{}{}
	}
	return d, nil
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
	mux.Handle("POST /admin/principals/{id}/mode", d.guard.ProtectRole(operator.RoleAdmin, http.HandlerFunc(d.handleSetMode)))
	if d.revoker != nil {
		mux.Handle("POST /admin/principals/{id}/revoke", d.guard.ProtectRole(operator.RoleAdmin, http.HandlerFunc(d.handleRevoke)))
	}
	d.managementRoutes(mux)

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
	v, err := d.build(r)
	if err != nil {
		d.fail(w, "building the table after revocation", err)
		return
	}
	v.Notice = fmt.Sprintf("Revoked %d active %s. New leases can still be issued.", n, pluralWord(n, "lease"))
	d.render(w, "table", v)
}

// view is what the templates render.
type view struct {
	BuildLabel       string
	OperatorLabel    string
	CanAdmin         bool
	CanOperate       bool
	CanManage        bool
	CanOnboard       bool
	CanCredentials   bool
	PricingLabel     string
	PricingDetail    string
	Warning          string
	Principals       []row
	Total            money.Nanos
	Notice           string
	Events           []eventRow
	EventFilter      eventFilterView
	ProviderSettings providerSettingsView
}

type eventFilterView struct {
	PrincipalID      string
	Kind             string
	Since            string
	Query            string
	Limit            int
	PrincipalOptions []filterOption
	RefreshURL       string
	NextURL          string
	HasMore          bool
}

type filterOption struct {
	Value    string
	Label    string
	Selected bool
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
	OverBudget       bool
	ActiveLeases     int
	RevokedLeases    int
	ExpiredLeases    int
	BudgetBlocks     int
	WouldBlockEvents int
	Status           string
	StatusClass      string
	LeaseSummary     string
	LastEvent        string
}

type eventRow struct {
	Kind          string
	KindClass     string
	PrincipalName string
	RunID         string
	Detail        string
	When          string
}

// handlePage renders the whole page.
func (d *Dashboard) handlePage(w http.ResponseWriter, r *http.Request) {
	v, err := d.build(r)
	if err != nil {
		d.fail(w, "building the dashboard", err)
		return
	}
	if v.CanOnboard || v.CanCredentials {
		v.ProviderSettings.Providers, err = d.providerStatuses(r.Context())
		if err != nil {
			d.fail(w, "reading provider credential status", err)
			return
		}
	}
	d.render(w, "dashboard.html", v)
}

// handleTable renders just the table, which is what htmx polls for.
func (d *Dashboard) handleTable(w http.ResponseWriter, r *http.Request) {
	v, err := d.build(r)
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

// build assembles the view and applies the event filters from the request.
func (d *Dashboard) build(r *http.Request) (view, error) {
	ctx := r.Context()
	summaries, err := d.store.PrincipalSummaries(ctx)
	if err != nil {
		return view{}, err
	}

	now := time.Now()
	eventFilter, eventFilterView := parseEventFilter(r, now)
	v := view{
		BuildLabel:    dashboardBuildLabel(d.version),
		PricingLabel:  d.pricingLabel,
		PricingDetail: d.pricingDetail,
		Warning:       d.warning,
		EventFilter:   eventFilterView,
	}
	if identity, ok := operator.IdentityFromContext(ctx); ok {
		v.OperatorLabel = identity.Name + " · " + string(identity.Role)
		v.CanOperate = identity.Role.Allows(operator.RoleOperator)
		v.CanAdmin = identity.Role.Allows(operator.RoleAdmin)
	}
	v.CanManage = v.CanOperate && d.manager != nil
	v.CanOnboard = v.CanAdmin && d.manager != nil && len(d.providers) > 0
	v.CanCredentials = v.CanAdmin && d.credentials != nil && len(d.providers) > 0

	for _, s := range summaries {
		v.Total += s.Spend
		status, statusClass := principalStatus(s)
		v.Principals = append(v.Principals, row{
			ID:               s.ID,
			Name:             s.Name,
			Mode:             s.Mode,
			Runs:             s.Runs,
			Entries:          s.Entries,
			EstimatedEntries: s.EstimatedEntries,
			Spend:            s.Spend,
			OverBudget:       s.OverBudgetRuns > 0,
			ActiveLeases:     s.ActiveLeases,
			RevokedLeases:    s.RevokedLeases,
			ExpiredLeases:    s.ExpiredLeases,
			BudgetBlocks:     s.BudgetBlocks,
			WouldBlockEvents: s.WouldBlockEvents,
			Status:           status,
			StatusClass:      statusClass,
			LeaseSummary:     leaseSummary(s),
			LastEvent:        relative(s.LastEvent, now),
		})
		v.EventFilter.PrincipalOptions = append(v.EventFilter.PrincipalOptions, filterOption{
			Value: s.ID, Label: s.Name, Selected: s.ID == v.EventFilter.PrincipalID,
		})
	}
	sort.Slice(v.EventFilter.PrincipalOptions, func(i, j int) bool {
		return strings.ToLower(v.EventFilter.PrincipalOptions[i].Label) <
			strings.ToLower(v.EventFilter.PrincipalOptions[j].Label)
	})

	queryFilter := eventFilter
	if queryFilter.Limit < maxEventLimit {
		queryFilter.Limit++
	}
	events, err := d.store.RecentOperationalEvents(ctx, queryFilter, now)
	if err != nil {
		return view{}, err
	}
	if len(events) > eventFilter.Limit {
		v.EventFilter.HasMore = true
		events = events[:eventFilter.Limit]
	}
	for _, event := range events {
		v.Events = append(v.Events, operationalEventRow(event, now))
	}
	v.EventFilter.RefreshURL = eventFilterURL("/table", v.EventFilter, eventFilter.Limit)
	if v.EventFilter.HasMore {
		nextLimit := min(eventFilter.Limit+eventPageSize, maxEventLimit)
		v.EventFilter.NextURL = eventFilterURL("/table", v.EventFilter, nextLimit)
	}
	return v, nil
}

const (
	eventPageSize = 20
	maxEventLimit = 200
)

var attentionEventKinds = []store.OperationalEventKind{
	store.EventBudgetBlocked,
	store.EventBudgetWouldBlock,
	store.EventLeaseRevoked,
	store.EventLeaseExpired,
}

func parseEventFilter(r *http.Request, now time.Time) (store.OperationalEventFilter, eventFilterView) {
	principalID := strings.TrimSpace(r.FormValue("event_agent"))
	query := strings.TrimSpace(r.FormValue("event_q"))

	kind := strings.TrimSpace(r.FormValue("event_kind"))
	if kind == "" {
		kind = "attention"
	}
	kinds := eventKinds(kind)
	if kinds == nil && kind != "all" {
		kind, kinds = "attention", attentionEventKinds
	}

	sinceName := strings.TrimSpace(r.FormValue("event_since"))
	if sinceName == "" {
		sinceName = "24h"
	}
	var since time.Time
	switch sinceName {
	case "1h":
		since = now.Add(-time.Hour)
	case "24h":
		since = now.Add(-24 * time.Hour)
	case "7d":
		since = now.Add(-7 * 24 * time.Hour)
	case "all":
	default:
		sinceName = "24h"
		since = now.Add(-24 * time.Hour)
	}

	limit, err := strconv.Atoi(r.FormValue("event_limit"))
	if err != nil || limit < eventPageSize {
		limit = eventPageSize
	}
	limit = min(limit, maxEventLimit)

	return store.OperationalEventFilter{
			PrincipalID: principalID,
			Kinds:       kinds,
			Query:       query,
			Since:       since,
			Limit:       limit,
		}, eventFilterView{
			PrincipalID: principalID,
			Kind:        kind,
			Since:       sinceName,
			Query:       query,
			Limit:       limit,
		}
}

func eventKinds(name string) []store.OperationalEventKind {
	switch name {
	case "attention":
		return attentionEventKinds
	case "blocked":
		return []store.OperationalEventKind{store.EventBudgetBlocked}
	case "would_block":
		return []store.OperationalEventKind{store.EventBudgetWouldBlock}
	case "revoked":
		return []store.OperationalEventKind{store.EventLeaseRevoked}
	case "expired":
		return []store.OperationalEventKind{store.EventLeaseExpired}
	case "allowed":
		return []store.OperationalEventKind{store.EventAllowed}
	case "all":
		return nil
	default:
		return nil
	}
}

func eventFilterURL(path string, filter eventFilterView, limit int) string {
	values := url.Values{
		"event_kind":  {filter.Kind},
		"event_since": {filter.Since},
	}
	if filter.PrincipalID != "" {
		values.Set("event_agent", filter.PrincipalID)
	}
	if filter.Query != "" {
		values.Set("event_q", filter.Query)
	}
	if limit > eventPageSize {
		values.Set("event_limit", strconv.Itoa(limit))
	}
	return path + "?" + values.Encode()
}

func dashboardBuildLabel(version string) string {
	if version == "" || version == "dev" {
		return "Local development build"
	}
	return "Build " + version
}

func priceBookLabel(revision string, effective time.Time) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 8 {
		revision = revision[:8]
	}
	if revision == "" {
		revision = "unknown"
	}
	if effective.IsZero() {
		return "Price book " + revision
	}
	return fmt.Sprintf("Price book %s · rates through %s", revision, effective.UTC().Format("2 Jan 2006"))
}

func priceBookDetail(loadedAt time.Time, providers, models int) string {
	detail := fmt.Sprintf("%d providers · %d price entries", providers, models)
	if loadedAt.IsZero() {
		return detail
	}
	return fmt.Sprintf("Loaded %s · %s", loadedAt.UTC().Format("2 Jan 2006 15:04 UTC"), detail)
}

func principalStatus(s store.PrincipalSummary) (string, string) {
	switch {
	case s.ActiveLeases > 0:
		return "Active", "ok"
	case s.RevokedLeases > 0:
		return "Revoked", "danger"
	case s.ExpiredLeases > 0:
		return "Expired", "muted"
	default:
		return "No leases", "muted"
	}
}

func leaseSummary(s store.PrincipalSummary) string {
	return fmt.Sprintf("%d active · %d revoked · %d expired",
		s.ActiveLeases, s.RevokedLeases, s.ExpiredLeases)
}

func operationalEventRow(event store.OperationalEvent, now time.Time) eventRow {
	row := eventRow{
		PrincipalName: event.PrincipalName,
		RunID:         event.RunID,
		When:          relative(&event.CreatedAt, now),
	}
	switch event.Kind {
	case store.EventAllowed:
		row.Kind, row.KindClass = "Allowed", "ok"
		row.Detail = fmt.Sprintf("%s/%s · $%s", event.Provider, event.Model, event.Amount.String())
	case store.EventBudgetBlocked:
		row.Kind, row.KindClass = "Budget blocked", "danger"
		row.Detail = fmt.Sprintf("needed $%s; $%s remaining", event.Amount.String(), event.Remaining.String())
	case store.EventBudgetWouldBlock:
		row.Kind, row.KindClass = "Would block", "warning"
		row.Detail = fmt.Sprintf("needed $%s; $%s remaining", event.Amount.String(), event.Remaining.String())
	case store.EventLeaseRevoked:
		row.Kind, row.KindClass = "Lease revoked", "danger"
		row.Detail = event.LeaseID
	case store.EventLeaseExpired:
		row.Kind, row.KindClass = "Lease expired", "muted"
		row.Detail = event.LeaseID
	}
	return row
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

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
