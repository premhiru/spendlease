package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/dashboard"
	"github.com/premhiru/spendlease/internal/gateway"
	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
)

type apiHarness struct {
	handler   http.Handler
	store     *sqlite.Store
	principal store.Principal
}

func newAPIHarness(t *testing.T, token string) *apiHarness {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, sqlite.InMemory, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, hash := store.NewPrincipalKey()
	principal := store.Principal{
		ID: store.NewPrincipalID(), Name: "orchestrator-agent", KeyHash: hash,
		Mode: store.ModeEnforce, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreatePrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	revocations := gateway.NewRevocationSet()
	api, err := New(Options{
		Store: st, Revoker: gateway.NewKillSwitch(st, revocations), Guard: dashboard.Guard{Token: token},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.Routes(mux)
	return &apiHarness{handler: mux, store: st, principal: principal}
}

func (h *apiHarness) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://localhost:4000"+path, reader)
	req.Host = "localhost:4000"
	req.RemoteAddr = "127.0.0.1:5000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		req.Header.Set(dashboard.AdminRequestHeader, "1")
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func decodeResponse[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return out
}

func TestRunAndLeaseLifecycle(t *testing.T) {
	h := newAPIHarness(t, "")

	created := h.request(t, http.MethodPost, "/api/v1/principals/"+h.principal.ID+"/runs", `{"budget_usd":"2.50"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	run := decodeResponse[runDTO](t, created)
	if run.PrincipalID != h.principal.ID || run.BudgetUSD != "2.50" || run.Status != store.RunActive {
		t.Fatalf("created run = %+v", run)
	}

	listed := h.request(t, http.MethodGet, "/api/v1/principals/"+h.principal.ID+"/runs", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(run.ID)) {
		t.Fatalf("list response = %d: %s", listed.Code, listed.Body.String())
	}

	budget := h.request(t, http.MethodGet, "/api/v1/runs/"+run.ID+"/budget", "")
	status := decodeResponse[budgetDTO](t, budget)
	if !status.SpendAllowed || status.Unlimited || status.EffectiveRemainingUSD != "2.50" || status.LimitingRunID != run.ID {
		t.Fatalf("budget = %+v", status)
	}

	issued := h.request(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/leases",
		`{"ttl_seconds":60,"providers":["openai","anthropic"],"ceiling_usd":"1.00"}`)
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue status = %d: %s", issued.Code, issued.Body.String())
	}
	lease := decodeResponse[leaseDTO](t, issued)
	if !store.LooksLikeLeaseToken(lease.Token) || lease.Status != "active" || len(lease.Providers) != 2 {
		t.Fatalf("issued lease = %+v", lease)
	}

	leases := h.request(t, http.MethodGet, "/api/v1/runs/"+run.ID+"/leases", "")
	if leases.Code != http.StatusOK || bytes.Contains(leases.Body.Bytes(), []byte(lease.Token)) || bytes.Contains(leases.Body.Bytes(), []byte("token_hash")) {
		t.Fatalf("lease list exposed secret or failed: %s", leases.Body.String())
	}

	revoked := h.request(t, http.MethodPost, "/api/v1/leases/"+lease.ID+"/revoke", `{}`)
	if got := decodeResponse[leaseDTO](t, revoked).Status; got != "revoked" {
		t.Fatalf("revoked lease status = %q", got)
	}

	closed := h.request(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/close", `{}`)
	if got := decodeResponse[runDTO](t, closed).Status; got != store.RunClosed {
		t.Fatalf("closed run status = %q", got)
	}
	refused := h.request(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/leases", `{"ttl_seconds":60}`)
	if refused.Code != http.StatusConflict {
		t.Fatalf("lease on closed run status = %d", refused.Code)
	}
	if got := refused.Header().Get("X-Spendlease-Error"); got != "run_closed" {
		t.Fatalf("X-Spendlease-Error = %q", got)
	}
}

func TestControlPlaneRejectsAmbiguousInput(t *testing.T) {
	h := newAPIHarness(t, "")
	path := "/api/v1/principals/" + h.principal.ID + "/runs"
	for name, body := range map[string]string{
		"unknown field": `{"budget_usd":"1.00","budegt_usd":"2.00"}`,
		"two objects":   `{"budget_usd":"1.00"}{"budget_usd":"2.00"}`,
		"negative":      `{"budget_usd":"-1.00"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := h.request(t, http.MethodPost, path, body)
			if got.Code != http.StatusBadRequest || got.Header().Get("X-Spendlease-Error") != "invalid_request" {
				t.Fatalf("response = %d %q: %s", got.Code, got.Header().Get("X-Spendlease-Error"), got.Body.String())
			}
		})
	}
	badSince := h.request(t, http.MethodGet, "/api/v1/ledger/export?since=yesterday", "")
	if badSince.Code != http.StatusBadRequest {
		t.Fatalf("bad since status = %d", badSince.Code)
	}
}

func TestControlPlaneUsesAdminGuard(t *testing.T) {
	h := newAPIHarness(t, "secret")
	req := httptest.NewRequest(http.MethodPost, "https://gateway.example/api/v1/principals/"+h.principal.ID+"/runs",
		strings.NewReader(`{"budget_usd":"1.00"}`))
	req.Host = "gateway.example"
	req.RemoteAddr = "203.0.113.10:5000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mutation without admin header = %d, want 403", rec.Code)
	}

	req.Header.Set(dashboard.AdminRequestHeader, "1")
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("guarded mutation = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLedgerVerifyAndExport(t *testing.T) {
	h := newAPIHarness(t, "")
	run := store.Run{
		ID: store.NewRunID(), PrincipalID: h.principal.ID, Budget: money.MustParseUSD("1.00"),
		Status: store.RunActive, CreatedAt: time.Now().UTC(),
	}
	if err := h.store.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := h.store.AppendLedger(context.Background(), ledger.Entry{
			RunID: run.ID, PrincipalID: h.principal.ID, Provider: "openai", Model: "gpt-4o-mini",
			InputTokens: int64(i + 1), OutputTokens: 2, Cost: money.Micro, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	verified := h.request(t, http.MethodGet, "/api/v1/ledger/verify", "")
	if verified.Code != http.StatusOK || !bytes.Contains(verified.Body.Bytes(), []byte(`"entries":2`)) {
		t.Fatalf("verify = %d: %s", verified.Code, verified.Body.String())
	}
	jsonExport := h.request(t, http.MethodGet, "/api/v1/ledger/export?format=json&run_id="+run.ID, "")
	if jsonExport.Code != http.StatusOK || !bytes.Contains(jsonExport.Body.Bytes(), []byte(`"cost_usd":"0.000001"`)) {
		t.Fatalf("JSON export = %d: %s", jsonExport.Code, jsonExport.Body.String())
	}
	csvExport := h.request(t, http.MethodGet, "/api/v1/ledger/export?format=csv", "")
	if csvExport.Code != http.StatusOK || !strings.HasPrefix(csvExport.Body.String(), "sequence,run_id,principal_id") {
		t.Fatalf("CSV export = %d: %s", csvExport.Code, csvExport.Body.String())
	}

	if _, err := h.store.DB().Exec(`DROP TRIGGER ledger_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE ledger SET model = 'tampered' WHERE seq = 1`); err != nil {
		t.Fatal(err)
	}
	invalid := h.request(t, http.MethodGet, "/api/v1/ledger/verify", "")
	if invalid.Code != http.StatusConflict || !bytes.Contains(invalid.Body.Bytes(), []byte(`"type":"ledger_invalid"`)) {
		t.Fatalf("tampered verify = %d: %s", invalid.Code, invalid.Body.String())
	}
}
