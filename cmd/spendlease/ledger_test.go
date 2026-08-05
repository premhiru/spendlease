package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
)

func seedLedgerCommandStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ctx := context.Background()
	st, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, keyHash := store.NewPrincipalKey()
	principal := store.Principal{
		ID: store.NewPrincipalID(), Name: "ledger-agent", KeyHash: keyHash,
		Mode: store.ModeEnforce, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreatePrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	run := store.Run{
		ID: store.NewRunID(), PrincipalID: principal.ID, Budget: money.MustParseUSD("1.00"),
		Status: store.RunActive, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendLedger(ctx, ledger.Entry{
		RunID: run.ID, PrincipalID: principal.ID, Provider: "openai", Model: "gpt-4o-mini",
		InputTokens: 10, OutputTokens: 5, Cost: money.MustParseUSD("0.001"), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLedgerVerifyCommand(t *testing.T) {
	t.Parallel()
	path := seedLedgerCommandStore(t)
	var stdout, stderr bytes.Buffer
	if err := runLedger([]string{"verify", "--store", path}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Ledger verified: 1 entries, head ") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestLedgerHelp(t *testing.T) {
	var out bytes.Buffer
	if err := runLedger([]string{"--help"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "spendlease ledger verify") {
		t.Fatalf("help = %q", out.String())
	}
}

func TestLedgerExportCommand(t *testing.T) {
	t.Parallel()
	path := seedLedgerCommandStore(t)
	for _, tt := range []struct {
		format string
		want   string
	}{
		{format: "json", want: `"cost_usd":"0.001"`},
		{format: "csv", want: "sequence,run_id,principal_id"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := runLedger([]string{"export", "--store", path, "--format", tt.format}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestLedgerReconcileCommand(t *testing.T) {
	t.Parallel()
	path := seedLedgerCommandStore(t)
	statementPath := filepath.Join(t.TempDir(), "statement.csv")
	now := time.Now().UTC()
	statement := "provider,model,usage_json,cost_usd,occurred_at,external_id\n" +
		fmt.Sprintf(`openai,gpt-4o-mini,"{""input_tokens"":10,""output_tokens"":5}",0.001,%s,`, now.Format(time.RFC3339Nano)) + "\n"
	if err := os.WriteFile(statementPath, []byte(statement), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"reconcile", "--store", path, "--statement", statementPath,
		"--since", now.Add(-time.Hour).Format(time.RFC3339),
		"--until", now.Add(time.Hour).Format(time.RFC3339),
	}
	var stdout, stderr bytes.Buffer
	if err := runLedger(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"status":"match"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}

	args = append(args, "--cost-tolerance", "0", "--fail-on-mismatch")
	statement = strings.Replace(statement, ",0.001,", ",0.02,", 1)
	if err := os.WriteFile(statementPath, []byte(statement), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runLedger(args, &stdout, &stderr); err == nil {
		t.Fatal("mismatched reconciliation returned success")
	}
	if !strings.Contains(stdout.String(), `"status":"mismatch"`) {
		t.Fatalf("mismatch report was not written: %q", stdout.String())
	}
}
