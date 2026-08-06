package dashboard

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/operator"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
)

var leaseTokenRE = regexp.MustCompile(`sll_[a-z2-7]+`)

type memoryCredentials struct {
	mu   sync.Mutex
	keys map[string]string
}

func (m *memoryCredentials) Put(_ context.Context, provider, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keys == nil {
		m.keys = make(map[string]string)
	}
	m.keys[provider] = key
	return nil
}

func (m *memoryCredentials) Delete(_ context.Context, provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, provider)
	return nil
}

func (m *memoryCredentials) Providers(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for name := range m.keys {
		names = append(names, name)
	}
	return names, nil
}

func (m *memoryCredentials) value(provider string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keys[provider]
}

type sqliteLeaseRevoker struct{ store *sqlite.Store }

func (r sqliteLeaseRevoker) RevokeLease(ctx context.Context, id string) (store.Lease, error) {
	if err := r.store.RevokeLease(ctx, id, time.Now().UTC()); err != nil {
		return store.Lease{}, err
	}
	return r.store.GetLease(ctx, id)
}

type managementHarness struct {
	handler     http.Handler
	store       *sqlite.Store
	credentials *memoryCredentials
}

func newManagementHarness(t *testing.T) *managementHarness {
	t.Helper()
	st, err := sqlite.Open(context.Background(), sqlite.InMemory, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	credentials := &memoryCredentials{keys: map[string]string{"openai": "configured-openai-key"}}
	d, err := New(Options{
		Store: st, Manager: st, LeaseRevoker: sqliteLeaseRevoker{st}, Credentials: credentials,
		Providers: []string{"openai", "kimi"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	d.Routes(mux)
	return &managementHarness{handler: mux, store: st, credentials: credentials}
}

func (h *managementHarness) form(t *testing.T, method, path string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	req := httptest.NewRequest(method, "http://localhost:4000"+path, body)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "localhost:4000"
	req.Header.Set(AdminRequestHeader, "1")
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func createAgentForm(name string) url.Values {
	return url.Values{
		"name": {name}, "mode": {"observe"}, "budget_usd": {"0.50"},
		"ttl_seconds": {"3600"}, "providers": {"openai"},
	}
}

func TestDashboardCreatesAgentRunAndOneTimeLease(t *testing.T) {
	h := newManagementHarness(t)
	rec := h.form(t, http.MethodPost, "/admin/agents", createAgentForm("checkout-agent"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	token := leaseTokenRE.FindString(rec.Body.String())
	if token == "" || strings.Contains(rec.Body.String(), "slk_") {
		t.Fatalf("onboarding response did not contain exactly the intended lease secret: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	for _, want := range []string{"Make the first request", "SPENDLEASE_LEASE_TOKEN", "Python", "JavaScript", "curl"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("onboarding response is missing %q", want)
		}
	}

	principals, err := h.store.ListPrincipals(context.Background())
	if err != nil || len(principals) != 1 {
		t.Fatalf("principals = %+v, err = %v", principals, err)
	}
	runs, err := h.store.ListRunsByPrincipal(context.Background(), principals[0].ID)
	if err != nil || len(runs) != 1 || runs[0].Budget != money.MustParseUSD("0.50") {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	leases, err := h.store.ListLeasesByRun(context.Background(), runs[0].ID)
	if err != nil || len(leases) != 1 || !store.SecretMatches(token, leases[0].TokenHash) {
		t.Fatalf("leases = %+v, err = %v", leases, err)
	}

	page := h.form(t, http.MethodGet, "/", nil).Body.String()
	if strings.Contains(page, token) {
		t.Fatal("the one-time token reappeared after onboarding")
	}
	for _, want := range []string{"Add an agent", "Provider keys", "Manage", "checkout-agent"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
}

func TestDashboardOnboardingRejectsUnsafeInputBeforeWriting(t *testing.T) {
	for name, change := range map[string]func(url.Values){
		"zero budget":      func(v url.Values) { v.Set("budget_usd", "0") },
		"unknown provider": func(v url.Values) { v.Set("providers", "made-up") },
		"control in name":  func(v url.Values) { v.Set("name", "bad\nname") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newManagementHarness(t)
			values := createAgentForm("agent")
			change(values)
			rec := h.form(t, http.MethodPost, "/admin/agents", values)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `role="alert"`) {
				t.Fatalf("response = %d: %s", rec.Code, rec.Body.String())
			}
			principals, _ := h.store.ListPrincipals(context.Background())
			if len(principals) != 0 {
				t.Fatalf("invalid form created %+v", principals)
			}
		})
	}
}

func TestDashboardOnboardingReportsDuplicateName(t *testing.T) {
	h := newManagementHarness(t)
	if rec := h.form(t, http.MethodPost, "/admin/agents", createAgentForm("same-name")); rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d: %s", rec.Code, rec.Body.String())
	}
	rec := h.form(t, http.MethodPost, "/admin/agents", createAgentForm("same-name"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("duplicate response = %d: %s", rec.Code, rec.Body.String())
	}
	principals, _ := h.store.ListPrincipals(context.Background())
	if len(principals) != 1 {
		t.Fatalf("duplicate form left %d principals", len(principals))
	}
}

func TestProviderKeyCanBeStoredButNeverEchoed(t *testing.T) {
	h := newManagementHarness(t)
	const secret = "moonshot-secret-marker"
	rec := h.form(t, http.MethodPost, "/admin/providers/kimi", url.Values{"api_key": {secret}})
	if rec.Code != http.StatusOK {
		t.Fatalf("store status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatal("provider settings echoed the plaintext key")
	}
	if got := h.credentials.value("kimi"); got != secret {
		t.Fatalf("stored key = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "Configured") {
		t.Error("provider did not become visibly configured")
	}

	rec = h.form(t, http.MethodDelete, "/admin/providers/kimi", nil)
	if rec.Code != http.StatusOK || h.credentials.value("kimi") != "" {
		t.Fatalf("delete response = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentAccessManagesRunAndLeaseLifecycle(t *testing.T) {
	h := newManagementHarness(t)
	if rec := h.form(t, http.MethodPost, "/admin/agents", createAgentForm("managed-agent")); rec.Code != http.StatusCreated {
		t.Fatalf("create agent = %d: %s", rec.Code, rec.Body.String())
	}
	principals, _ := h.store.ListPrincipals(context.Background())
	principalID := principals[0].ID

	access := h.form(t, http.MethodGet, "/admin/principals/"+principalID+"/access", nil)
	if access.Code != http.StatusOK || !strings.Contains(access.Body.String(), "Issue new lease") {
		t.Fatalf("access response = %d: %s", access.Code, access.Body.String())
	}
	created := h.form(t, http.MethodPost, "/admin/principals/"+principalID+"/runs", url.Values{"budget_usd": {"2.00"}})
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "Created run") {
		t.Fatalf("run response = %d: %s", created.Code, created.Body.String())
	}
	runs, _ := h.store.ListRunsByPrincipal(context.Background(), principalID)
	if len(runs) != 2 {
		t.Fatalf("run count = %d, want 2", len(runs))
	}
	var run store.Run
	for _, candidate := range runs {
		if candidate.Budget == money.MustParseUSD("2.00") {
			run = candidate
		}
	}
	if run.ID == "" {
		t.Fatalf("new run not found in %+v", runs)
	}

	issued := h.form(t, http.MethodPost, "/admin/runs/"+run.ID+"/leases", url.Values{
		"ttl_seconds": {"900"}, "ceiling_usd": {"0.25"}, "providers": {"kimi"},
	})
	if issued.Code != http.StatusCreated || leaseTokenRE.FindString(issued.Body.String()) == "" {
		t.Fatalf("lease response = %d: %s", issued.Code, issued.Body.String())
	}
	leases, _ := h.store.ListLeasesByRun(context.Background(), run.ID)
	if len(leases) != 1 || leases[0].Ceiling != money.MustParseUSD("0.25") {
		t.Fatalf("leases = %+v", leases)
	}

	revoked := h.form(t, http.MethodPost, "/admin/leases/"+leases[0].ID+"/revoke", url.Values{})
	gotLease, _ := h.store.GetLease(context.Background(), leases[0].ID)
	if revoked.Code != http.StatusOK || gotLease.RevokedAt == nil {
		t.Fatalf("revoke response = %d: %s", revoked.Code, revoked.Body.String())
	}
	closed := h.form(t, http.MethodPost, "/admin/runs/"+run.ID+"/close", url.Values{})
	gotRun, _ := h.store.GetRun(context.Background(), run.ID)
	if closed.Code != http.StatusOK || gotRun.Status != store.RunClosed {
		t.Fatalf("close response = %d: %s", closed.Code, closed.Body.String())
	}
}

func TestManagementUIRespectsOperatorRoles(t *testing.T) {
	for _, tt := range []struct {
		role         operator.Role
		wantOnboard  bool
		wantManage   bool
		createStatus int
		manageStatus int
	}{
		{role: operator.RoleViewer, createStatus: http.StatusForbidden, manageStatus: http.StatusForbidden},
		{role: operator.RoleOperator, wantManage: true, createStatus: http.StatusForbidden, manageStatus: http.StatusOK},
		{role: operator.RoleAdmin, wantOnboard: true, wantManage: true, createStatus: http.StatusCreated, manageStatus: http.StatusOK},
	} {
		t.Run(string(tt.role), func(t *testing.T) {
			st, err := sqlite.Open(context.Background(), sqlite.InMemory, sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			_, principalHash := store.NewPrincipalKey()
			p := store.Principal{ID: store.NewPrincipalID(), Name: "existing", KeyHash: principalHash, Mode: store.ModeObserve, CreatedAt: time.Now()}
			if err := st.CreatePrincipal(context.Background(), p); err != nil {
				t.Fatal(err)
			}

			token, hash := operator.NewToken()
			operators := &operatorGuardStore{op: operator.Operator{
				ID: "opr_test", Name: "test", TokenHash: hash, Role: tt.role, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}}
			credentials := &memoryCredentials{keys: map[string]string{"openai": "configured"}}
			d, err := New(Options{
				Store: st, Manager: st, LeaseRevoker: sqliteLeaseRevoker{st}, Credentials: credentials,
				Providers: []string{"openai"}, Guard: Guard{Operators: operators},
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			d.Routes(mux)
			request := func(method, path string, values url.Values) *httptest.ResponseRecorder {
				var body io.Reader
				if values != nil {
					body = strings.NewReader(values.Encode())
				}
				req := httptest.NewRequest(method, "https://gateway.example"+path, body)
				req.Host = "gateway.example"
				req.RemoteAddr = "203.0.113.9:5000"
				req.Header.Set("Authorization", "Bearer "+token)
				if method != http.MethodGet {
					req.Header.Set(AdminRequestHeader, "1")
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				return rec
			}

			page := request(http.MethodGet, "/", nil)
			body := page.Body.String()
			if strings.Contains(body, "Add an agent") != tt.wantOnboard || strings.Contains(body, "Provider keys") != tt.wantOnboard {
				t.Fatalf("%s onboarding visibility is wrong", tt.role)
			}
			if strings.Contains(body, "Agent access") != tt.wantManage {
				t.Fatalf("%s management visibility is wrong", tt.role)
			}
			if got := request(http.MethodPost, "/admin/agents", createAgentForm("new-agent")).Code; got != tt.createStatus {
				t.Fatalf("create status = %d, want %d", got, tt.createStatus)
			}
			if got := request(http.MethodGet, "/admin/principals/"+p.ID+"/access", nil).Code; got != tt.manageStatus {
				t.Fatalf("manage status = %d, want %d", got, tt.manageStatus)
			}
		})
	}
}
