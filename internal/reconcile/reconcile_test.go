package reconcile

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/billing"
	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
)

func TestBuildFindsCostAndUsageDifferences(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	entries := []ledger.Entry{{
		Provider: "openai", Model: "gpt-5", CreatedAt: since.Add(time.Hour),
		Cost: money.MustParseUSD("1.00"), ExternalID: "req_1",
		Usage: billing.TokenUsage(100, 20, 0, 0, 10),
	}}
	statement := []StatementEntry{{
		Provider: "openai", Model: "gpt-5", OccurredAt: since.Add(time.Hour),
		Cost: money.MustParseUSD("1.02"), ExternalID: "req_1",
		Usage: billing.TokenUsage(110, 20, 0, 0, 10),
	}}
	report, err := Build(entries, statement, Options{
		Since: since, Until: since.Add(24 * time.Hour), CostTolerance: money.Cent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Mismatched() || len(report.Groups) != 1 {
		t.Fatalf("report = %#v", report)
	}
	group := report.Groups[0]
	if group.Status != "cost_mismatch" || group.CostDeltaUSD != "0.02" {
		t.Fatalf("group = %#v", group)
	}
	if group.UsageDelta[billing.UnitInputTokens] != 10 || group.MatchedExternalIDs != 1 {
		t.Fatalf("usage/external match = %#v/%d", group.UsageDelta, group.MatchedExternalIDs)
	}
}

func TestBuildRejectsUnrepresentableDeltas(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	entries := []ledger.Entry{
		{Provider: "openai", Model: "gpt-5", CreatedAt: since, Cost: money.Nanos(math.MaxInt64)},
		{Provider: "openai", Model: "gpt-5", CreatedAt: since, Usage: billing.Usage{"requests": math.MaxInt64}},
	}
	statement := []StatementEntry{
		{Provider: "openai", Model: "gpt-5", OccurredAt: since, Cost: money.Nanos(math.MaxInt64)},
		{Provider: "openai", Model: "gpt-5", OccurredAt: since, Cost: 1, Usage: billing.Usage{"requests": math.MaxInt64}},
	}
	_, err := Build(entries, statement, Options{Since: since, Until: since.Add(time.Hour)})
	if err == nil || !strings.Contains(err.Error(), "cost total overflows") {
		t.Fatalf("Build error = %v, want cost overflow", err)
	}

	entries = []ledger.Entry{{
		Provider: "openai", Model: "gpt-5", CreatedAt: since,
		Usage: billing.Usage{"requests": math.MaxInt64},
	}}
	statement = []StatementEntry{{
		Provider: "openai", Model: "gpt-5", OccurredAt: since,
		Usage: billing.Usage{"requests": math.MaxInt64},
	}, {
		Provider: "openai", Model: "gpt-5", OccurredAt: since,
		Usage: billing.Usage{"requests": 1},
	}}
	_, err = Build(entries, statement, Options{Since: since, Until: since.Add(time.Hour)})
	if err == nil || !strings.Contains(err.Error(), "usage total overflows") {
		t.Fatalf("Build error = %v, want usage overflow", err)
	}
}

func TestReadStatementCSV(t *testing.T) {
	raw := "provider,model,usage_json,cost_usd,occurred_at,external_id\n" +
		`openai,gpt-5,"{""input_tokens"":100,""output_tokens"":4}",0.01,2026-08-01T01:00:00Z,req_1` + "\n"
	entries, err := ReadStatementCSV(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Usage[billing.UnitInputTokens] != 100 || entries[0].ExternalID != "req_1" {
		t.Fatalf("entries = %#v", entries)
	}
}
