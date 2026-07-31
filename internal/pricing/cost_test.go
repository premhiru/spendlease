package pricing

import (
	"testing"

	"github.com/premhiru/spendlease/internal/money"
)

func TestRate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tokens     int64
		perMillion string
		want       string
	}{
		{name: "zero tokens", tokens: 0, perMillion: "2.50", want: "0.00"},
		{name: "negative tokens", tokens: -5, perMillion: "2.50", want: "0.00"},
		{name: "zero rate", tokens: 1000, perMillion: "0.00", want: "0.00"},

		// The canonical example: gpt-4o input at $2.50 per million.
		{name: "exactly one million tokens", tokens: 1_000_000, perMillion: "2.50", want: "2.50"},
		{name: "one thousand tokens", tokens: 1_000, perMillion: "2.50", want: "0.0025"},
		{name: "one token", tokens: 1, perMillion: "2.50", want: "0.0000025"},
		{name: "ten million tokens", tokens: 10_000_000, perMillion: "2.50", want: "25.00"},

		{name: "cheap model", tokens: 1_000_000, perMillion: "0.15", want: "0.15"},
		{name: "expensive model", tokens: 1_000_000, perMillion: "600.00", want: "600.00"},

		// Whole millions plus a remainder, which is the path the split
		// arithmetic exists for.
		{name: "millions plus remainder", tokens: 2_500_000, perMillion: "2.50", want: "6.25"},
		{name: "just over a million", tokens: 1_000_001, perMillion: "2.50", want: "2.5000025"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := rate(tt.tokens, money.MustParseUSD(tt.perMillion))
			if got != money.MustParseUSD(tt.want) {
				t.Errorf("rate(%d, %s) = %s, want %s", tt.tokens, tt.perMillion, got, tt.want)
			}
		})
	}
}

// TestRateDoesNotOverflow is why the calculation splits the token count.
// The naive tokens*perMillion would overflow int64 well inside these ranges.
func TestRateDoesNotOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tokens     int64
		perMillion string
		want       string
	}{
		{
			name:   "a billion tokens at the most expensive published rate",
			tokens: 1_000_000_000, perMillion: "600.00", want: "600000.00",
		},
		{
			name:   "a hundred billion cheap tokens",
			tokens: 100_000_000_000, perMillion: "0.15", want: "15000.00",
		},
		{
			name:   "a trillion tokens",
			tokens: 1_000_000_000_000, perMillion: "2.50", want: "2500000.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := rate(tt.tokens, money.MustParseUSD(tt.perMillion))
			if got < 0 {
				t.Fatalf("rate(%d, %s) = %s: overflowed to a negative cost",
					tt.tokens, tt.perMillion, got)
			}
			if got != money.MustParseUSD(tt.want) {
				t.Errorf("rate(%d, %s) = %s, want %s", tt.tokens, tt.perMillion, got, tt.want)
			}
		})
	}
}

// TestRateRoundsHalfUp documents the rounding choice. Truncating would bias
// every charge downwards, and a spend limiter that consistently under-counts
// is the wrong kind of wrong.
func TestRateRoundsHalfUp(t *testing.T) {
	t.Parallel()

	// $0.000000001 per million tokens is one nanodollar per million: a single
	// token is a millionth of a nanodollar, which must round rather than
	// vanish or be truncated inconsistently.
	oneNanoPerMillion := money.MustParseUSD("0.000000001")

	tests := []struct {
		tokens int64
		want   money.Nanos
	}{
		{tokens: 1, want: 0},         // 0.000001 nanos, rounds to 0
		{tokens: 499_999, want: 0},   // just under half a nano
		{tokens: 500_000, want: 1},   // exactly half, rounds up
		{tokens: 999_999, want: 1},   // just under one nano
		{tokens: 1_000_000, want: 1}, // exactly one nano
	}

	for _, tt := range tests {
		got := rate(tt.tokens, oneNanoPerMillion)
		if got != tt.want {
			t.Errorf("rate(%d, 1 nano/MTok) = %d nanos, want %d", tt.tokens, int64(got), int64(tt.want))
		}
	}
}

func TestPriceCost(t *testing.T) {
	t.Parallel()

	p := Price{
		InputPer1M:  money.MustParseUSD("2.50"),
		OutputPer1M: money.MustParseUSD("10.00"),
	}

	tests := []struct {
		name  string
		usage Usage
		want  string
	}{
		{name: "nothing used", usage: Usage{}, want: "0.00"},
		{name: "input only", usage: Usage{InputTokens: 1_000_000}, want: "2.50"},
		{name: "output only", usage: Usage{OutputTokens: 1_000_000}, want: "10.00"},
		{name: "both", usage: Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, want: "12.50"},
		{
			name:  "a realistic single call",
			usage: Usage{InputTokens: 1_200, OutputTokens: 800},
			want:  "0.011", // 1200*2.50/1e6 = 0.003 ; 800*10/1e6 = 0.008
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := p.Cost(tt.usage); got != money.MustParseUSD(tt.want) {
				t.Errorf("Cost(%+v) = %s, want %s", tt.usage, got, tt.want)
			}
		})
	}

	t.Run("input and output can be priced separately", func(t *testing.T) {
		t.Parallel()

		if got := p.InputCost(1_000_000); got != money.MustParseUSD("2.50") {
			t.Errorf("InputCost = %s", got)
		}
		if got := p.OutputCost(1_000_000); got != money.MustParseUSD("10.00") {
			t.Errorf("OutputCost = %s", got)
		}
	})
}

func TestPriceCostUsesCacheAndLongContextRates(t *testing.T) {
	t.Parallel()

	p := Price{
		InputPer1M:           money.MustParseUSD("2.00"),
		CachedInputPer1M:     money.MustParseUSD("0.20"),
		CacheWrite5mPer1M:    money.MustParseUSD("2.50"),
		CacheWrite1hPer1M:    money.MustParseUSD("4.00"),
		OutputPer1M:          money.MustParseUSD("12.00"),
		LongContextThreshold: 200_000,
		LongInputPer1M:       money.MustParseUSD("4.00"),
		LongCachedInputPer1M: money.MustParseUSD("0.40"),
		LongCacheWritePer1M:  money.MustParseUSD("5.00"),
		LongOutputPer1M:      money.MustParseUSD("18.00"),
	}

	short := Usage{
		InputTokens: 50_000, CachedInputTokens: 25_000,
		CacheWrite5mTokens: 10_000, CacheWrite1hTokens: 5_000,
		OutputTokens: 10_000,
	}
	if got := p.Cost(short); got != money.MustParseUSD("0.27") {
		t.Errorf("short cost = %s, want 0.27", got)
	}

	long := Usage{InputTokens: 100_000, CachedInputTokens: 100_000, OutputTokens: 10_000}
	if got := p.Cost(long); got != money.MustParseUSD("0.62") {
		t.Errorf("long cost = %s, want 0.62", got)
	}
}

// TestCostIsAdditive is the property the ledger depends on: charging a
// conversation in pieces must total the same as charging it in one go, or
// per-run spend would drift from the invoice.
func TestCostIsAdditive(t *testing.T) {
	t.Parallel()

	p := Price{
		InputPer1M:  money.MustParseUSD("2.50"),
		OutputPer1M: money.MustParseUSD("10.00"),
	}

	var pieces money.Nanos
	var totalIn, totalOut int64
	for i := 1; i <= 100; i++ {
		in, out := int64(i*13), int64(i*7)
		pieces += p.Cost(Usage{InputTokens: in, OutputTokens: out})
		totalIn += in
		totalOut += out
	}

	whole := p.Cost(Usage{InputTokens: totalIn, OutputTokens: totalOut})

	// Rounding each piece can differ from rounding the total by at most one
	// nanodollar per piece; a hundred pieces means at most 100 nanodollars,
	// which is a ten-millionth of a cent.
	diff := pieces - whole
	if diff < 0 {
		diff = -diff
	}
	if diff > 100 {
		t.Errorf("summing 100 charges gave %s, charging the total gave %s (differ by %d nanos)",
			pieces, whole, int64(diff))
	}
}
