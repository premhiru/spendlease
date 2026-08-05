package ledger

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/billing"
	"github.com/premhiru/spendlease/internal/money"
)

func TestJSONExportIncludesItemizedUsageAndPricingProvenance(t *testing.T) {
	at := time.Date(2026, 8, 1, 2, 3, 4, 5, time.UTC)
	entry := Entry{
		HashVersion: HashVersionUsage, Seq: 1, RunID: "run_1", PrincipalID: "p_1",
		Provider: "anthropic", Model: "claude-sonnet-4", InputTokens: 12, OutputTokens: 3,
		Usage: billing.TokenUsage(12, 4, 2, 0, 3), Cost: money.MustParseUSD("0.0001"),
		ExternalID: "req_1", PricingRevision: "2026-08-01", PriceEffective: at, CreatedAt: at,
	}.Seal(GenesisHash)

	var buf bytes.Buffer
	if err := WriteJSON(&buf, []Entry{entry}); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version int           `json:"version"`
		Entries []ExportEntry `json:"entries"`
	}
	if err := json.Unmarshal(buf.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 2 || len(document.Entries) != 1 {
		t.Fatalf("export document = %#v", document)
	}
	got := document.Entries[0]
	if got.Usage[billing.UnitCachedInputTokens] != 4 || got.ExternalID != "req_1" ||
		got.PricingRevision != "2026-08-01" || got.PriceEffective == "" {
		t.Fatalf("export entry = %#v", got)
	}
}

func TestCSVExportHasVersionTwoColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"usage_json", "external_id", "pricing_revision", "price_effective", "hash_version"}
	for _, name := range want {
		found := false
		for _, column := range rows[0] {
			found = found || column == name
		}
		if !found {
			t.Errorf("CSV header has no %q column: %v", name, rows[0])
		}
	}
}
