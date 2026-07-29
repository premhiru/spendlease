package pricing_test

import (
	"os"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/pricing"
)

// loadShipped loads the real /pricing directory.
//
// These tests are the safety net under the contribution path described in
// CONTRIBUTING.md: a price book PR needs no Go, so the schema has to be
// enforced here or not at all.
func loadShipped(t *testing.T) *pricing.Book {
	t.Helper()

	b, err := pricing.Load(os.DirFS("../.."), "pricing", pricing.Options{})
	if err != nil {
		t.Fatalf("the shipped price book does not load: %v", err)
	}
	return b
}

func TestShippedPriceBookLoads(t *testing.T) {
	t.Parallel()

	b := loadShipped(t)

	providers := b.Providers()
	if len(providers) < 2 {
		t.Fatalf("providers = %v, want at least openai and anthropic", providers)
	}

	want := map[string]bool{"openai": false, "anthropic": false}
	for _, p := range providers {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("the price book has no %s provider", name)
		}
	}
}

// TestShippedPricesAreSane catches the realistic contribution mistakes: a
// decimal point in the wrong place, or output priced below input.
func TestShippedPricesAreSane(t *testing.T) {
	t.Parallel()

	b := loadShipped(t)
	now := time.Now()

	// A price above this is almost certainly a units error, for example
	// entering a per-thousand-token price into a per-million field.
	ceiling := money.MustParseUSD("1000.00")

	for _, provider := range b.Providers() {
		for _, model := range b.Models(provider, now) {
			p, known := b.Lookup(provider, model, now)
			if !known {
				t.Errorf("%s/%s is listed but does not resolve", provider, model)
				continue
			}

			switch {
			case p.InputPer1M <= 0:
				t.Errorf("%s/%s has a non-positive input price (%s)", provider, model, p.InputPer1M)
			case p.OutputPer1M <= 0:
				t.Errorf("%s/%s has a non-positive output price (%s)", provider, model, p.OutputPer1M)
			case p.InputPer1M > ceiling:
				t.Errorf("%s/%s input %s exceeds %s; check the units", provider, model, p.InputPer1M, ceiling)
			case p.OutputPer1M > ceiling:
				t.Errorf("%s/%s output %s exceeds %s; check the units", provider, model, p.OutputPer1M, ceiling)
			case p.OutputPer1M < p.InputPer1M:
				t.Errorf("%s/%s prices output (%s) below input (%s), which no vendor does",
					provider, model, p.OutputPer1M, p.InputPer1M)
			}

			if p.DefaultMaxTokens <= 0 {
				t.Errorf("%s/%s has no positive default_max_tokens", provider, model)
			}
			if p.Source == "" {
				t.Errorf("%s/%s has no source URL", provider, model)
			}
		}
	}
}

// TestKnownModelsArePriced pins the models the documentation and the README
// name, so a rename or a bad merge is caught rather than silently falling back
// to the estimated rate.
func TestKnownModelsArePriced(t *testing.T) {
	t.Parallel()

	b := loadShipped(t)
	at := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		provider, model string
		wantIn, wantOut string
	}{
		{provider: "openai", model: "gpt-4o", wantIn: "2.50", wantOut: "10.00"},
		{provider: "openai", model: "gpt-4o-mini", wantIn: "0.15", wantOut: "0.60"},
		{provider: "anthropic", model: "claude-sonnet-5", wantIn: "2.00", wantOut: "10.00"},
		{provider: "anthropic", model: "claude-opus-5", wantIn: "5.00", wantOut: "25.00"},
		{provider: "anthropic", model: "claude-haiku-4-5-20251001", wantIn: "1.00", wantOut: "5.00"},
		// Alias resolution, which vendors publish alongside dated ids.
		{provider: "anthropic", model: "claude-haiku-4-5", wantIn: "1.00", wantOut: "5.00"},
		{provider: "anthropic", model: "claude-opus-4-5", wantIn: "5.00", wantOut: "25.00"},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			t.Parallel()

			p, known := b.Lookup(tt.provider, tt.model, at)
			if !known {
				t.Fatalf("%s/%s is not in the price book; it would fall back to the estimated rate",
					tt.provider, tt.model)
			}
			if p.InputPer1M != money.MustParseUSD(tt.wantIn) {
				t.Errorf("input = %s, want %s", p.InputPer1M, tt.wantIn)
			}
			if p.OutputPer1M != money.MustParseUSD(tt.wantOut) {
				t.Errorf("output = %s, want %s", p.OutputPer1M, tt.wantOut)
			}
		})
	}
}

// TestSonnet5IntroductoryPricingExpires exercises the dated-supersession
// mechanism against real shipped data rather than a fixture. Anthropic
// publishes an introductory rate through 2026-08-31 and a standard rate from
// 2026-09-01; the price book has to switch on the right day.
func TestSonnet5IntroductoryPricingExpires(t *testing.T) {
	t.Parallel()

	b := loadShipped(t)

	tests := []struct {
		name    string
		at      time.Time
		wantIn  string
		wantOut string
	}{
		{
			name:   "introductory rate applies in July",
			at:     time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC),
			wantIn: "2.00", wantOut: "10.00",
		},
		{
			name:   "still introductory on the last day",
			at:     time.Date(2026, time.August, 31, 23, 59, 59, 0, time.UTC),
			wantIn: "2.00", wantOut: "10.00",
		},
		{
			name:   "standard rate from the first of September",
			at:     time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
			wantIn: "3.00", wantOut: "15.00",
		},
		{
			name:   "and afterwards",
			at:     time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC),
			wantIn: "3.00", wantOut: "15.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, known := b.Lookup("anthropic", "claude-sonnet-5", tt.at)
			if !known {
				t.Fatal("claude-sonnet-5 is not priced")
			}
			if p.InputPer1M != money.MustParseUSD(tt.wantIn) {
				t.Errorf("at %s input = %s, want %s", tt.at.Format(time.DateOnly), p.InputPer1M, tt.wantIn)
			}
			if p.OutputPer1M != money.MustParseUSD(tt.wantOut) {
				t.Errorf("at %s output = %s, want %s", tt.at.Format(time.DateOnly), p.OutputPer1M, tt.wantOut)
			}
		})
	}
}

// TestWorkedExampleMatchesVendorArithmetic checks the cost calculation against
// a worked example published by the vendor, which is the closest thing to an
// independent oracle available.
//
// From Anthropic's pricing documentation: a session using Claude Opus 5 with
// 50,000 input tokens and 15,000 output tokens costs $0.25 + $0.375 in tokens.
func TestWorkedExampleMatchesVendorArithmetic(t *testing.T) {
	t.Parallel()

	b := loadShipped(t)
	at := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)

	p, known := b.Lookup("anthropic", "claude-opus-5", at)
	if !known {
		t.Fatal("claude-opus-5 is not priced")
	}

	if got := p.InputCost(50_000); got != money.MustParseUSD("0.25") {
		t.Errorf("50,000 input tokens = %s, want 0.25", got)
	}
	if got := p.OutputCost(15_000); got != money.MustParseUSD("0.375") {
		t.Errorf("15,000 output tokens = %s, want 0.375", got)
	}
	if got := p.Cost(pricing.Usage{InputTokens: 50_000, OutputTokens: 15_000}); got != money.MustParseUSD("0.625") {
		t.Errorf("combined = %s, want 0.625", got)
	}
}

// TestFortyThousandDollarLoop is the README's motivating scenario, priced.
// It is here because the number in the README should be defensible rather
// than rhetorical.
func TestFortyThousandDollarLoop(t *testing.T) {
	t.Parallel()

	b := loadShipped(t)
	at := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)

	// Twelve unattended hours at one call per second, with a 4k prompt and a
	// 1k completion each time.
	const calls = 12 * 60 * 60
	usage := pricing.Usage{InputTokens: 4_000, OutputTokens: 1_000}

	scenarios := []struct {
		model   string
		agents  int64
		comment string
	}{
		{model: "gpt-4o", agents: 1, comment: "one agent, mid-priced model"},
		{model: "gpt-4o", agents: 50, comment: "a fleet of fifty"},
		{model: "o1-pro", agents: 1, comment: "one agent, most expensive model"},
	}

	var worst money.Nanos
	for _, s := range scenarios {
		p, known := b.Lookup("openai", s.model, at)
		if !known {
			t.Fatalf("%s is not priced", s.model)
		}
		perCall := p.Cost(usage)
		if perCall.IsZero() {
			t.Fatalf("a realistic %s call priced at zero", s.model)
		}
		total := perCall * money.Nanos(calls) * money.Nanos(s.agents)
		if total > worst {
			worst = total
		}
		t.Logf("%-34s %s x %d calls x %d agent(s) = %s", s.comment, perCall, calls, s.agents, total)
	}

	// The README's $40,000 figure has to be reachable by a plausible
	// unattended overnight loop, or it is rhetoric rather than a motivating
	// example. It is not reachable on a cheap model with a single agent,
	// which is worth knowing when describing the scenario.
	if worst < money.MustParseUSD("40000.00") {
		t.Errorf("the worst plausible overnight loop costs %s, below the $40,000 the README cites", worst)
	}
}
