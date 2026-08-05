package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/operator"
	"github.com/premhiru/spendlease/internal/store"
)

const maxManagementForm = 64 << 10

// AgentManager is the persistence surface used by the dashboard's guided
// setup and lease workspace. It deliberately mirrors the public control-plane
// operations so the browser remains another client of the same domain model.
type AgentManager interface {
	CreatePrincipalRunLease(context.Context, store.Principal, store.Run, store.Lease) error
	GetPrincipal(context.Context, string) (store.Principal, error)
	CreateRun(context.Context, store.Run) error
	GetRun(context.Context, string) (store.Run, error)
	ListRunsByPrincipal(context.Context, string) ([]store.Run, error)
	CloseRun(context.Context, string) error
	CreateLease(context.Context, store.Lease) error
	ListLeasesByRun(context.Context, string) ([]store.Lease, error)
}

// LeaseRevoker invalidates one lease in durable storage and every local
// revocation cache.
type LeaseRevoker interface {
	RevokeLease(context.Context, string) (store.Lease, error)
}

// ProviderCredentials is the write-only credential surface exposed to the
// dashboard. There is intentionally no Get method: a browser can replace or
// remove a vendor key, but it can never retrieve one.
type ProviderCredentials interface {
	Put(context.Context, string, string) error
	Delete(context.Context, string) error
}

// ProviderStatusSource reports only provider names; it never exposes stored
// credential material.
type ProviderStatusSource interface {
	Providers(context.Context) ([]string, error)
}

type providerStatus struct {
	Name       string
	Label      string
	Configured bool
	Endpoint   string
}

type providerSettingsView struct {
	Providers []providerStatus
	Notice    string
	Error     string
}

type createdAgentView struct {
	Error       string
	Name        string
	PrincipalID string
	RunID       string
	Mode        store.Mode
	Budget      string
	Token       string
	ExpiresAt   string
	Endpoints   []providerStatus
}

type agentAccessView struct {
	Error         string
	Notice        string
	PrincipalID   string
	PrincipalName string
	Runs          []accessRun
	Providers     []providerStatus
	Issued        *issuedLeaseView
}

type accessRun struct {
	ID        string
	Budget    string
	Status    store.RunStatus
	CreatedAt string
	Active    bool
	Leases    []accessLease
}

type accessLease struct {
	ID        string
	Providers string
	Ceiling   string
	ExpiresAt string
	Status    string
	Active    bool
}

type issuedLeaseView struct {
	Token     string
	RunID     string
	ExpiresAt string
	Endpoints []providerStatus
}

func (d *Dashboard) managementRoutes(mux *http.ServeMux) {
	if d.manager != nil {
		mux.Handle("POST /admin/agents", d.guard.ProtectRole(operator.RoleAdmin, http.HandlerFunc(d.handleCreateAgent)))
		mux.Handle("GET /admin/principals/{id}/access", d.guard.ProtectRole(operator.RoleOperator, http.HandlerFunc(d.handleAgentAccess)))
		mux.Handle("POST /admin/principals/{id}/runs", d.guard.ProtectRole(operator.RoleOperator, http.HandlerFunc(d.handleCreateRun)))
		mux.Handle("POST /admin/runs/{id}/leases", d.guard.ProtectRole(operator.RoleOperator, http.HandlerFunc(d.handleIssueLease)))
		mux.Handle("POST /admin/runs/{id}/close", d.guard.ProtectRole(operator.RoleOperator, http.HandlerFunc(d.handleCloseRun)))
		if d.leaseRevoker != nil {
			mux.Handle("POST /admin/leases/{id}/revoke", d.guard.ProtectRole(operator.RoleOperator, http.HandlerFunc(d.handleRevokeLease)))
		}
	}
	if d.credentials != nil {
		mux.Handle("POST /admin/providers/{provider}", d.guard.ProtectRole(operator.RoleAdmin, http.HandlerFunc(d.handleSetProvider)))
		mux.Handle("DELETE /admin/providers/{provider}", d.guard.ProtectRole(operator.RoleAdmin, http.HandlerFunc(d.handleDeleteProvider)))
	}
}

func normalizeProviderNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	var out []string
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (d *Dashboard) providerStatuses(ctx context.Context) ([]providerStatus, error) {
	configured := make(map[string]struct{})
	if d.credentialStatus != nil {
		names, err := d.credentialStatus.Providers(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			configured[name] = struct{}{}
		}
	}
	statuses := make([]providerStatus, 0, len(d.providers))
	for _, name := range d.providers {
		_, ok := configured[name]
		statuses = append(statuses, providerStatus{Name: name, Label: providerLabel(name), Configured: ok, Endpoint: providerPath(name)})
	}
	return statuses, nil
}

func providerLabel(name string) string {
	switch name {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "kimi":
		return "Kimi"
	case "deepseek":
		return "DeepSeek"
	case "xai":
		return "xAI"
	case "gemini":
		return "Gemini"
	case "zai":
		return "Z.AI"
	default:
		return name
	}
}

func providerPath(name string) string {
	switch name {
	case "openai":
		return "/v1"
	case "anthropic":
		return ""
	case "kimi":
		return "/kimi/v1"
	case "deepseek":
		return "/deepseek/v1"
	case "xai":
		return "/xai/v1"
	case "gemini":
		return "/gemini/v1beta/openai"
	case "zai":
		return "/zai/api/paas/v4"
	default:
		return "/" + name
	}
}

func (d *Dashboard) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if err := parseManagementForm(w, r); err != nil {
		d.renderManagement(w, "onboarding-result", createdAgentView{Error: userMessage(err)}, http.StatusUnprocessableEntity)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := validateAgentName(name); err != nil {
		d.renderManagement(w, "onboarding-result", createdAgentView{Error: userMessage(err)}, http.StatusUnprocessableEntity)
		return
	}
	mode := store.Mode(strings.TrimSpace(r.FormValue("mode")))
	if !mode.Valid() {
		d.renderManagement(w, "onboarding-result", createdAgentView{Error: "Mode must be observe or enforce."}, http.StatusUnprocessableEntity)
		return
	}
	budget, err := positiveUSD(r.FormValue("budget_usd"), "budget")
	if err != nil {
		d.renderManagement(w, "onboarding-result", createdAgentView{Error: userMessage(err)}, http.StatusUnprocessableEntity)
		return
	}
	ttl, err := parseTTL(r.FormValue("ttl_seconds"))
	if err != nil {
		d.renderManagement(w, "onboarding-result", createdAgentView{Error: userMessage(err)}, http.StatusUnprocessableEntity)
		return
	}
	providers, err := d.selectedProviders(r)
	if err != nil {
		d.renderManagement(w, "onboarding-result", createdAgentView{Error: userMessage(err)}, http.StatusUnprocessableEntity)
		return
	}

	_, principalHash := store.NewPrincipalKey()
	token, tokenHash := store.NewLeaseToken()
	now := time.Now().UTC()
	principal := store.Principal{ID: store.NewPrincipalID(), Name: name, KeyHash: principalHash, Mode: mode, CreatedAt: now}
	run := store.Run{ID: store.NewRunID(), PrincipalID: principal.ID, Budget: budget, Status: store.RunActive, CreatedAt: now}
	lease := store.Lease{
		ID: store.NewLeaseID(), RunID: run.ID, TokenHash: tokenHash, Providers: providers,
		ExpiresAt: now.Add(ttl), CreatedAt: now,
	}
	if err := d.manager.CreatePrincipalRunLease(r.Context(), principal, run, lease); err != nil {
		message := "Could not create the agent. Try again."
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrConflict) {
			message, status = "An agent with that name already exists.", http.StatusConflict
		}
		d.logger.Error("creating dashboard agent", "name", name, "error", err)
		d.renderManagement(w, "onboarding-result", createdAgentView{Error: message}, status)
		return
	}

	d.logger.Info("dashboard agent created", "principal", principal.ID, "run", run.ID, "providers", providers)
	d.renderManagement(w, "onboarding-result", createdAgentView{
		Name: name, PrincipalID: principal.ID, RunID: run.ID, Mode: mode, Budget: budget.String(),
		Token: token, ExpiresAt: lease.ExpiresAt.Format("2 Jan 2006 15:04 UTC"), Endpoints: d.endpointStatuses(r, providers),
	}, http.StatusCreated)
}

func validateAgentName(name string) error {
	if name == "" {
		return errors.New("agent name is required")
	}
	if len(name) > 100 {
		return errors.New("agent name must be 100 characters or fewer")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("agent name cannot contain control characters")
	}
	return nil
}

func positiveUSD(raw, label string) (money.Nanos, error) {
	amount, err := money.ParseUSD(raw)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("%s must be a positive USD amount", label)
	}
	return amount, nil
}

func nonnegativeUSD(raw, label string) (money.Nanos, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	amount, err := money.ParseUSD(raw)
	if err != nil || amount < 0 {
		return 0, fmt.Errorf("%s must be a non-negative USD amount", label)
	}
	return amount, nil
}

func parseTTL(raw string) (time.Duration, error) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds < 60 || seconds > int64((30*24*time.Hour)/time.Second) {
		return 0, errors.New("lease duration must be between one minute and 30 days")
	}
	return time.Duration(seconds) * time.Second, nil
}

func (d *Dashboard) selectedProviders(r *http.Request) ([]string, error) {
	selected := normalizeProviderNames(r.Form["providers"])
	if len(selected) == 0 {
		return nil, errors.New("select at least one provider")
	}
	for _, name := range selected {
		if _, ok := d.providerSet[name]; !ok {
			return nil, fmt.Errorf("provider %q is not available on this gateway", name)
		}
	}
	return selected, nil
}

func (d *Dashboard) endpointStatuses(r *http.Request, names []string) []providerStatus {
	base := "http://" + r.Host
	if r.TLS != nil {
		base = "https://" + r.Host
	}
	configured := make(map[string]struct{})
	if d.credentialStatus != nil {
		if values, err := d.credentialStatus.Providers(r.Context()); err == nil {
			for _, name := range values {
				configured[name] = struct{}{}
			}
		}
	}
	result := make([]providerStatus, 0, len(names))
	for _, name := range names {
		_, ok := configured[name]
		result = append(result, providerStatus{Name: name, Label: providerLabel(name), Configured: ok, Endpoint: base + providerPath(name)})
	}
	return result
}

func (d *Dashboard) handleAgentAccess(w http.ResponseWriter, r *http.Request) {
	v, err := d.buildAccess(r.Context(), r.PathValue("id"))
	if err != nil {
		d.managementStoreError(w, "loading agent access", err)
		return
	}
	d.renderManagement(w, "agent-access", v, http.StatusOK)
}

func (d *Dashboard) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	principalID := r.PathValue("id")
	if err := parseManagementForm(w, r); err != nil {
		d.renderAccessError(w, r, principalID, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	budget, err := positiveUSD(r.FormValue("budget_usd"), "budget")
	if err != nil {
		d.renderAccessError(w, r, principalID, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	if _, err := d.manager.GetPrincipal(r.Context(), principalID); err != nil {
		d.managementStoreError(w, "reading agent", err)
		return
	}
	run := store.Run{ID: store.NewRunID(), PrincipalID: principalID, Budget: budget, Status: store.RunActive, CreatedAt: time.Now().UTC()}
	if err := d.manager.CreateRun(r.Context(), run); err != nil {
		d.renderAccessError(w, r, principalID, "Could not create the run.", http.StatusInternalServerError)
		return
	}
	d.renderAccessNotice(w, r, principalID, "Created run "+run.ID+" with a $"+budget.String()+" budget.")
}

func (d *Dashboard) handleIssueLease(w http.ResponseWriter, r *http.Request) {
	run, err := d.manager.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		d.managementStoreError(w, "reading run", err)
		return
	}
	if err := parseManagementForm(w, r); err != nil {
		d.renderAccessError(w, r, run.PrincipalID, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	if run.Status != store.RunActive {
		d.renderAccessError(w, r, run.PrincipalID, "Closed runs cannot receive a new lease.", http.StatusConflict)
		return
	}
	ttl, err := parseTTL(r.FormValue("ttl_seconds"))
	if err != nil {
		d.renderAccessError(w, r, run.PrincipalID, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	providers, err := d.selectedProviders(r)
	if err != nil {
		d.renderAccessError(w, r, run.PrincipalID, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	ceiling, err := nonnegativeUSD(r.FormValue("ceiling_usd"), "lease ceiling")
	if err != nil {
		d.renderAccessError(w, r, run.PrincipalID, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	token, hash := store.NewLeaseToken()
	now := time.Now().UTC()
	lease := store.Lease{
		ID: store.NewLeaseID(), RunID: run.ID, TokenHash: hash, Providers: providers, Ceiling: ceiling,
		ExpiresAt: now.Add(ttl), CreatedAt: now,
	}
	if err := d.manager.CreateLease(r.Context(), lease); err != nil {
		d.renderAccessError(w, r, run.PrincipalID, "Could not issue the lease.", http.StatusInternalServerError)
		return
	}
	v, err := d.buildAccess(r.Context(), run.PrincipalID)
	if err != nil {
		d.managementStoreError(w, "refreshing agent access", err)
		return
	}
	v.Issued = &issuedLeaseView{
		Token: token, RunID: run.ID, ExpiresAt: lease.ExpiresAt.Format("2 Jan 2006 15:04 UTC"), Endpoints: d.endpointStatuses(r, providers),
	}
	d.renderManagement(w, "agent-access", v, http.StatusCreated)
}

func (d *Dashboard) handleCloseRun(w http.ResponseWriter, r *http.Request) {
	run, err := d.manager.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		d.managementStoreError(w, "reading run", err)
		return
	}
	if err := d.manager.CloseRun(r.Context(), run.ID); err != nil {
		d.renderAccessError(w, r, run.PrincipalID, "Could not close the run.", http.StatusInternalServerError)
		return
	}
	d.renderAccessNotice(w, r, run.PrincipalID, "Closed run "+run.ID+". Its leases can no longer spend.")
}

func (d *Dashboard) handleRevokeLease(w http.ResponseWriter, r *http.Request) {
	lease, err := d.leaseRevoker.RevokeLease(r.Context(), r.PathValue("id"))
	if err != nil {
		d.managementStoreError(w, "revoking lease", err)
		return
	}
	run, err := d.manager.GetRun(r.Context(), lease.RunID)
	if err != nil {
		d.managementStoreError(w, "reading revoked lease run", err)
		return
	}
	d.renderAccessNotice(w, r, run.PrincipalID, "Revoked lease "+lease.ID+".")
}

func (d *Dashboard) buildAccess(ctx context.Context, principalID string) (agentAccessView, error) {
	principal, err := d.manager.GetPrincipal(ctx, principalID)
	if err != nil {
		return agentAccessView{}, err
	}
	providers, err := d.providerStatuses(ctx)
	if err != nil && d.credentialStatus != nil {
		return agentAccessView{}, err
	}
	runs, err := d.manager.ListRunsByPrincipal(ctx, principalID)
	if err != nil {
		return agentAccessView{}, err
	}
	now := time.Now()
	v := agentAccessView{PrincipalID: principal.ID, PrincipalName: principal.Name, Providers: providers}
	if len(providers) == 0 {
		for _, name := range d.providers {
			v.Providers = append(v.Providers, providerStatus{Name: name, Label: providerLabel(name), Endpoint: providerPath(name)})
		}
	}
	for _, run := range runs {
		rv := accessRun{
			ID: run.ID, Budget: run.Budget.String(), Status: run.Status,
			CreatedAt: run.CreatedAt.UTC().Format("2 Jan 2006 15:04 UTC"), Active: run.Status == store.RunActive,
		}
		leases, err := d.manager.ListLeasesByRun(ctx, run.ID)
		if err != nil {
			return agentAccessView{}, err
		}
		for _, lease := range leases {
			status := "active"
			if lease.RevokedAt != nil {
				status = "revoked"
			} else if !lease.Active(now) {
				status = "expired"
			}
			scope := strings.Join(lease.Providers, ", ")
			if scope == "" {
				scope = "all providers"
			}
			rv.Leases = append(rv.Leases, accessLease{
				ID: lease.ID, Providers: scope, Ceiling: lease.Ceiling.String(),
				ExpiresAt: lease.ExpiresAt.UTC().Format("2 Jan 2006 15:04 UTC"), Status: status, Active: status == "active",
			})
		}
		v.Runs = append(v.Runs, rv)
	}
	return v, nil
}

func (d *Dashboard) renderAccessError(w http.ResponseWriter, r *http.Request, principalID, message string, status int) {
	v, err := d.buildAccess(r.Context(), principalID)
	if err != nil {
		d.managementStoreError(w, "refreshing agent access", err)
		return
	}
	v.Error = message
	d.renderManagement(w, "agent-access", v, status)
}

func (d *Dashboard) renderAccessNotice(w http.ResponseWriter, r *http.Request, principalID, message string) {
	v, err := d.buildAccess(r.Context(), principalID)
	if err != nil {
		d.managementStoreError(w, "refreshing agent access", err)
		return
	}
	v.Notice = message
	d.renderManagement(w, "agent-access", v, http.StatusOK)
}

func (d *Dashboard) handleSetProvider(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if _, ok := d.providerSet[provider]; !ok {
		d.renderProviderError(w, r, "That provider is not available on this gateway.", http.StatusNotFound)
		return
	}
	if err := parseManagementForm(w, r); err != nil {
		d.renderProviderError(w, r, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	key := strings.TrimSpace(r.FormValue("api_key"))
	if key == "" {
		d.renderProviderError(w, r, "API key is required.", http.StatusUnprocessableEntity)
		return
	}
	if err := d.credentials.Put(r.Context(), provider, key); err != nil {
		d.logger.Error("storing provider credential", "provider", provider, "error", err)
		d.renderProviderError(w, r, "Could not store the provider key.", http.StatusInternalServerError)
		return
	}
	d.renderProviderNotice(w, r, "Stored the "+providerLabel(provider)+" API key, encrypted at rest.")
}

func (d *Dashboard) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if _, ok := d.providerSet[provider]; !ok {
		d.renderProviderError(w, r, "That provider is not available on this gateway.", http.StatusNotFound)
		return
	}
	if err := d.credentials.Delete(r.Context(), provider); err != nil {
		d.logger.Error("removing provider credential", "provider", provider, "error", err)
		d.renderProviderError(w, r, "Could not remove the provider key.", http.StatusInternalServerError)
		return
	}
	d.renderProviderNotice(w, r, "Removed the "+providerLabel(provider)+" API key.")
}

func (d *Dashboard) renderProviderError(w http.ResponseWriter, r *http.Request, message string, status int) {
	providers, err := d.providerStatuses(r.Context())
	if err != nil {
		d.managementStoreError(w, "reading provider credential status", err)
		return
	}
	d.renderManagement(w, "provider-settings", providerSettingsView{Providers: providers, Error: message}, status)
}

func (d *Dashboard) renderProviderNotice(w http.ResponseWriter, r *http.Request, message string) {
	providers, err := d.providerStatuses(r.Context())
	if err != nil {
		d.managementStoreError(w, "reading provider credential status", err)
		return
	}
	d.renderManagement(w, "provider-settings", providerSettingsView{Providers: providers, Notice: message}, http.StatusOK)
}

func parseManagementForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxManagementForm)
	if err := r.ParseForm(); err != nil {
		return errors.New("the form could not be read")
	}
	return nil
}

func userMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "The form could not be read."
	}
	return strings.ToUpper(message[:1]) + message[1:] + "."
}

func (d *Dashboard) managementStoreError(w http.ResponseWriter, doing string, err error) {
	d.logger.Error(doing, "error", err)
	status := http.StatusInternalServerError
	message := "The dashboard could not complete that operation."
	if errors.Is(err, store.ErrNotFound) {
		status, message = http.StatusNotFound, "The requested agent, run, or lease no longer exists."
	}
	http.Error(w, message, status)
}

func (d *Dashboard) renderManagement(w http.ResponseWriter, name string, data any, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := d.tmpl.ExecuteTemplate(w, name, data); err != nil {
		d.logger.Error("rendering dashboard management", "template", name, "error", err)
	}
}
