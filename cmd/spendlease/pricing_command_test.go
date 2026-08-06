package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPricingListFiltersAndEmitsJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"pricing", "list", "--provider", "kimi", "--at", "2026-08-06", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var entries []pricingEntryOutput
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("JSON: %v\n%s", err, stdout.String())
	}
	if len(entries) == 0 {
		t.Fatal("Kimi list is empty")
	}
	for _, entry := range entries {
		if entry.Provider != "kimi" || entry.Verified != "2026-08-06" || entry.Source == "" {
			t.Fatalf("unexpected entry: %+v", entry)
		}
	}
}

func TestPricingShowIncludesRatesAndEvidence(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"pricing", "show", "--at", "2026-08-06", "openai/gpt-5.4-nano"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"gpt-5.4-nano", "Verified:", "2026-08-06", "developers.openai.com"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, stdout.String())
		}
	}
}

func TestPricingVerifyPassesCurrentAndFailsStale(t *testing.T) {
	for _, tt := range []struct {
		name string
		at   string
		code int
		want string
	}{
		{name: "current", at: "2026-08-06", code: 0, want: "CURRENT"},
		{name: "stale", at: "2026-10-01", code: 1, want: "STALE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"pricing", "verify", "--at", tt.at, "--max-age", "45d"}, &stdout, &stderr)
			if code != tt.code {
				t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, tt.code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("output does not contain %q:\n%s", tt.want, stdout.String())
			}
		})
	}
}

func TestPricingVerifyFlagsMissingEvidence(t *testing.T) {
	dir := t.TempDir()
	body := `version: 2
effective: 2026-08-01
providers:
  openai:
    source: https://example.invalid/pricing
    models:
      test-model:
        input_per_1m: 1
        output_per_1m: 2
        default_max_tokens: 10
`
	if err := os.WriteFile(filepath.Join(dir, "prices.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"pricing", "verify", "--pricing", dir, "--at", "2026-08-06"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "unverified") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestPricingVerifyFlagsFutureDatedEvidence(t *testing.T) {
	dir := t.TempDir()
	body := `version: 2
effective: 2026-08-01
verified: 2026-08-07
providers:
  openai:
    source: https://example.invalid/pricing
    models:
      test-model:
        input_per_1m: 1
        output_per_1m: 2
        default_max_tokens: 10
`
	if err := os.WriteFile(filepath.Join(dir, "prices.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"pricing", "verify", "--pricing", dir, "--at", "2026-08-06"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "future-dated") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestPricingRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"pricing", "show", "not-a-model-ref"},
		{"pricing", "verify", "--max-age", "0d"},
		{"pricing", "list", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v exit = %d, want 2; stderr=%s", args, code, stderr.String())
		}
	}
}
