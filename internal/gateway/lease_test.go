package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

func seedGatewayLease(t *testing.T, h *recordingHarness, providers []string, ceiling money.Nanos) (string, store.Lease) {
	t.Helper()
	ctx := context.Background()
	run := store.Run{ID: store.NewRunID(), PrincipalID: h.principal.ID, Budget: money.MustParseUSD("10.00"), Status: store.RunActive, CreatedAt: time.Now()}
	if err := h.store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	token, hash := store.NewLeaseToken()
	lease := store.Lease{ID: store.NewLeaseID(), RunID: run.ID, TokenHash: hash, Providers: providers, Ceiling: ceiling, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}
	if err := h.store.CreateLease(ctx, lease); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return token, lease
}

func leaseRequest(t *testing.T, h *recordingHarness, token, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp
}

func TestLeaseAuthenticationScopeAndAttribution(t *testing.T) {
	t.Parallel()
	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}, store.ModeEnforce, money.MustParseUSD("10.00"))
	token, lease := seedGatewayLease(t, h, []string{"openai"}, 0)

	if resp := leaseRequest(t, h, token, "/v1/messages", `{"model":"claude-sonnet-5"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anthropic status = %d, want 403", resp.StatusCode)
	}
	if resp := leaseRequest(t, h, token, "/v1/chat/completions", `{"model":"gpt-4o","max_tokens":10}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("openai status = %d, want 200", resp.StatusCode)
	}
	var got string
	if err := h.store.DB().QueryRow(`SELECT lease_id FROM reservations ORDER BY created_at DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("lease attribution: %v", err)
	}
	if got != lease.ID {
		t.Errorf("reservation lease = %s, want %s", got, lease.ID)
	}
}

func TestLeaseCeilingAndKillSwitch(t *testing.T) {
	t.Parallel()
	h := newRecordingHarnessWith(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) }, store.ModeEnforce, money.MustParseUSD("10.00"))
	token, lease := seedGatewayLease(t, h, []string{"openai"}, money.MustParseUSD("0.001"))
	if resp := leaseRequest(t, h, token, "/v1/chat/completions", `{"model":"gpt-4o","max_tokens":1000}`); resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("ceiling status = %d, want 402", resp.StatusCode)
	}

	start := time.Now()
	ks := NewKillSwitch(h.store, h.revocations)
	if n, err := ks.RevokePrincipal(context.Background(), h.principal.ID); err != nil || n != 1 {
		t.Fatalf("revoke = (%d,%v), want (1,nil)", n, err)
	}
	if time.Since(start) >= time.Second {
		t.Fatal("kill switch took one second or longer")
	}
	if !h.revocations.Revoked(lease.TokenHash) {
		t.Fatal("lease hash was not added to in-memory revocation set")
	}
	if resp := leaseRequest(t, h, token, "/v1/chat/completions", `{"model":"gpt-4o"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked status = %d, want 401", resp.StatusCode)
	}
}
