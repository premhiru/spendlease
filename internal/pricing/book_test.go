package pricing

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/premhiru/spendlease/internal/money"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// testFS builds an in-memory price book.
func testFS(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out["pricing/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

const basicBook = `
version: 1
effective: 2026-01-01
providers:
  openai:
    source: https://example.invalid/openai
    models:
      gpt-4o:
        input_per_1m: 2.50
        output_per_1m: 10.00
        default_max_tokens: 4096
      gpt-4o-mini:
        input_per_1m: 0.15
        output_per_1m: 0.60
        default_max_tokens: 4096
  anthropic:
    source: https://example.invalid/anthropic
    models:
      claude-sonnet-5:
        input_per_1m: 2.00
        output_per_1m: 10.00
        default_max_tokens: 8192
      claude-opus-4-5-20251101:
        aliases: [claude-opus-4-5]
        input_per_1m: 5.00
        output_per_1m: 25.00
        default_max_tokens: 8192
`

func loadTestBook(t *testing.T, files map[string]string, opts Options) *Book {
	t.Helper()

	b, err := Load(testFS(files), "pricing", opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func TestLookup(t *testing.T) {
	t.Parallel()

	b := loadTestBook(t, map[string]string{"base.yaml": basicBook}, Options{})
	at := date(2026, time.July, 1)

	tests := []struct {
		name       string
		provider   string
		model      string
		wantKnown  bool
		wantInput  string
		wantOutput string
		wantMax    int64
	}{
		{
			name: "exact model", provider: "openai", model: "gpt-4o",
			wantKnown: true, wantInput: "2.50", wantOutput: "10.00", wantMax: 4096,
		},
		{
			name: "cheap model", provider: "openai", model: "gpt-4o-mini",
			wantKnown: true, wantInput: "0.15", wantOutput: "0.60", wantMax: 4096,
		},
		{
			name: "other provider", provider: "anthropic", model: "claude-sonnet-5",
			wantKnown: true, wantInput: "2.00", wantOutput: "10.00", wantMax: 8192,
		},
		{
			name: "alias resolves to its canonical entry", provider: "anthropic", model: "claude-opus-4-5",
			wantKnown: true, wantInput: "5.00", wantOutput: "25.00", wantMax: 8192,
		},
		{
			name: "unknown model falls back", provider: "openai", model: "gpt-9-imaginary",
			wantKnown: false, wantInput: "15.00", wantOutput: "75.00", wantMax: 4096,
		},
		{
			name: "unknown provider falls back", provider: "cohere", model: "command-r",
			wantKnown: false, wantInput: "15.00", wantOutput: "75.00", wantMax: 4096,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, known := b.Lookup(tt.provider, tt.model, at)
			if known != tt.wantKnown {
				t.Errorf("known = %v, want %v", known, tt.wantKnown)
			}
			if got.InputPer1M != money.MustParseUSD(tt.wantInput) {
				t.Errorf("input = %s, want %s", got.InputPer1M, tt.wantInput)
			}
			if got.OutputPer1M != money.MustParseUSD(tt.wantOutput) {
				t.Errorf("output = %s, want %s", got.OutputPer1M, tt.wantOutput)
			}
			if got.DefaultMaxTokens != tt.wantMax {
				t.Errorf("default_max_tokens = %d, want %d", got.DefaultMaxTokens, tt.wantMax)
			}
			if got.Estimated == tt.wantKnown {
				t.Errorf("Estimated = %v for a known=%v lookup", got.Estimated, known)
			}
		})
	}
}

// TestEffectiveDatesSupersede is the mechanism that lets a scheduled price
// change ship as a new file: before the date the old price applies, after it
// the new one, and no other model is disturbed.
func TestEffectiveDatesSupersede(t *testing.T) {
	t.Parallel()

	const later = `
version: 1
effective: 2026-09-01
providers:
  anthropic:
    source: https://example.invalid/anthropic
    models:
      claude-sonnet-5:
        input_per_1m: 3.00
        output_per_1m: 15.00
        default_max_tokens: 8192
`

	b := loadTestBook(t, map[string]string{
		"base.yaml":  basicBook,
		"later.yaml": later,
	}, Options{})

	tests := []struct {
		name      string
		at        time.Time
		wantInput string
	}{
		{name: "before the change", at: date(2026, time.August, 31), wantInput: "2.00"},
		{name: "on the day of the change", at: date(2026, time.September, 1), wantInput: "3.00"},
		{name: "after the change", at: date(2026, time.December, 25), wantInput: "3.00"},
		{name: "long before either file", at: date(2025, time.January, 1), wantInput: "15.00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, _ := b.Lookup("anthropic", "claude-sonnet-5", tt.at)
			if got.InputPer1M != money.MustParseUSD(tt.wantInput) {
				t.Errorf("at %s input = %s, want %s", tt.at.Format(time.DateOnly), got.InputPer1M, tt.wantInput)
			}
		})
	}

	t.Run("other models are untouched by the newer file", func(t *testing.T) {
		t.Parallel()

		got, known := b.Lookup("openai", "gpt-4o", date(2026, time.December, 25))
		if !known || got.InputPer1M != money.MustParseUSD("2.50") {
			t.Errorf("gpt-4o = %s (known=%v), want 2.50", got.InputPer1M, known)
		}
	})
}

// TestPricesAreParsedExactly is the guard on ADR-0003: a YAML decoder that
// went through float64 would pass most of these and fail the sub-cent ones.
func TestPricesAreParsedExactly(t *testing.T) {
	t.Parallel()

	const book = `
version: 1
effective: 2026-01-01
providers:
  test:
    source: https://example.invalid
    models:
      a:
        input_per_1m: 0.10
        output_per_1m: 0.20
        default_max_tokens: 1
      b:
        input_per_1m: 2.50
        output_per_1m: 0.0000025
        default_max_tokens: 1
      c:
        input_per_1m: "3.00"
        output_per_1m: $4.50
        default_max_tokens: 1
      d:
        input_per_1m: 0.000000001
        output_per_1m: 9223372035.00
        default_max_tokens: 1
`

	b := loadTestBook(t, map[string]string{"x.yaml": book}, Options{})
	at := date(2026, time.June, 1)

	tests := []struct{ model, in, out string }{
		{"a", "0.10", "0.20"},
		{"b", "2.50", "0.0000025"},
		{"c", "3.00", "4.50"},
		{"d", "0.000000001", "9223372035.00"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()

			p, known := b.Lookup("test", tt.model, at)
			if !known {
				t.Fatalf("model %s not found", tt.model)
			}
			if p.InputPer1M != money.MustParseUSD(tt.in) {
				t.Errorf("input = %d nanos (%s), want %s", int64(p.InputPer1M), p.InputPer1M, tt.in)
			}
			if p.OutputPer1M != money.MustParseUSD(tt.out) {
				t.Errorf("output = %d nanos (%s), want %s", int64(p.OutputPer1M), p.OutputPer1M, tt.out)
			}
		})
	}
}

func TestValidationRejectsBadFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name:     "wrong version",
			body:     "version: 99\neffective: 2026-01-01\nproviders:\n  a:\n    source: x\n    models:\n      m: {input_per_1m: 1, output_per_1m: 1, default_max_tokens: 1}\n",
			contains: "version 99",
		},
		{
			name:     "no effective date",
			body:     "version: 1\nproviders:\n  a:\n    source: x\n    models:\n      m: {input_per_1m: 1, output_per_1m: 1, default_max_tokens: 1}\n",
			contains: "no effective date",
		},
		{
			name:     "no providers",
			body:     "version: 1\neffective: 2026-01-01\nproviders: {}\n",
			contains: "no providers",
		},
		{
			name:     "missing source url",
			body:     "version: 1\neffective: 2026-01-01\nproviders:\n  a:\n    models:\n      m: {input_per_1m: 1, output_per_1m: 1, default_max_tokens: 1}\n",
			contains: "no source URL",
		},
		{
			name:     "no models",
			body:     "version: 1\neffective: 2026-01-01\nproviders:\n  a:\n    source: x\n    models: {}\n",
			contains: "no models",
		},
		{
			name:     "zero default_max_tokens would reserve unbounded",
			body:     "version: 1\neffective: 2026-01-01\nproviders:\n  a:\n    source: x\n    models:\n      m: {input_per_1m: 1, output_per_1m: 1, default_max_tokens: 0}\n",
			contains: "must be positive",
		},
		{
			name:     "negative rate",
			body:     "version: 1\neffective: 2026-01-01\nproviders:\n  a:\n    source: x\n    models:\n      m: {input_per_1m: -1, output_per_1m: 1, default_max_tokens: 1}\n",
			contains: "negative",
		},
		{
			name:     "rate is not a number",
			body:     "version: 1\neffective: 2026-01-01\nproviders:\n  a:\n    source: x\n    models:\n      m: {input_per_1m: cheap, output_per_1m: 1, default_max_tokens: 1}\n",
			contains: "invalid amount",
		},
		{
			name:     "rate given as a list",
			body:     "version: 1\neffective: 2026-01-01\nproviders:\n  a:\n    source: x\n    models:\n      m: {input_per_1m: [1, 2], output_per_1m: 1, default_max_tokens: 1}\n",
			contains: "expected a number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(testFS(map[string]string{"bad.yaml": tt.body}), "pricing", Options{})
			if err == nil {
				t.Fatal("Load accepted an invalid price file")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q does not mention %q", err, tt.contains)
			}
		})
	}
}

func TestLoadRequiresAtLeastOneFile(t *testing.T) {
	t.Parallel()

	_, err := Load(testFS(map[string]string{"readme.txt": "not yaml"}), "pricing", Options{})
	if !errors.Is(err, ErrNoPrices) {
		t.Errorf("error = %v, want ErrNoPrices", err)
	}
}

// TestUnknownModelWarnsOnce covers the rule that an unknown model must be
// loud, without a retry loop flooding the log with the same line.
func TestUnknownModelWarnsOnce(t *testing.T) {
	t.Parallel()

	var warned []string
	b := loadTestBook(t, map[string]string{"base.yaml": basicBook}, Options{
		Warn: func(provider, model string) { warned = append(warned, provider+"/"+model) },
	})

	at := date(2026, time.July, 1)
	for i := 0; i < 5; i++ {
		b.Lookup("openai", "gpt-9-imaginary", at)
	}
	b.Lookup("openai", "another-unknown", at)

	if len(warned) != 2 {
		t.Fatalf("warned %d times (%v), want 2 distinct warnings", len(warned), warned)
	}
	if warned[0] != "openai/gpt-9-imaginary" || warned[1] != "openai/another-unknown" {
		t.Errorf("warnings = %v", warned)
	}
}

// TestUnknownModelNeverCostsZero is the specific failure the fallback exists
// to prevent.
func TestUnknownModelNeverCostsZero(t *testing.T) {
	t.Parallel()

	b := loadTestBook(t, map[string]string{"base.yaml": basicBook}, Options{})

	cost, known := b.Cost("openai", "totally-made-up", Usage{InputTokens: 1000, OutputTokens: 1000}, date(2026, time.July, 1))
	if known {
		t.Fatal("an invented model was reported as known")
	}
	if cost.IsZero() {
		t.Fatal("an unknown model cost nothing; a retry loop against it would be invisible")
	}
	// The fallback is deliberately expensive, not a token amount.
	if cost < money.MustParseUSD("0.05") {
		t.Errorf("fallback cost %s is suspiciously cheap for 2000 tokens", cost)
	}
}

func TestDisableFallback(t *testing.T) {
	t.Parallel()

	b := loadTestBook(t, map[string]string{"base.yaml": basicBook}, Options{DisableFallback: true})
	at := date(2026, time.July, 1)

	if _, err := b.Price("openai", "gpt-4o", at); err != nil {
		t.Errorf("a known model errored: %v", err)
	}
	_, err := b.Price("openai", "made-up", at)
	if !errors.Is(err, ErrUnknownModel) {
		t.Errorf("error = %v, want ErrUnknownModel", err)
	}
}

// TestReloadIsAtomicAndSafe covers hot reload: a bad file must not replace
// good prices, and concurrent lookups must never see a half-loaded book.
func TestReloadIsAtomicAndSafe(t *testing.T) {
	t.Parallel()

	fsys := testFS(map[string]string{"base.yaml": basicBook})
	b, err := Load(fsys, "pricing", Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	at := date(2026, time.July, 1)

	// Concurrent readers while a reload happens.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p, known := b.Lookup("openai", "gpt-4o", at)
			if !known || p.InputPer1M != money.MustParseUSD("2.50") {
				t.Errorf("lookup during reload saw %s (known=%v)", p.InputPer1M, known)
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		if err := b.Reload(); err != nil {
			t.Errorf("Reload: %v", err)
		}
	}
	<-done

	t.Run("a broken file leaves the previous prices in place", func(t *testing.T) {
		fsys["pricing/broken.yaml"] = &fstest.MapFile{Data: []byte("version: 99\n")}

		if err := b.Reload(); err == nil {
			t.Fatal("Reload accepted a broken file")
		}
		p, known := b.Lookup("openai", "gpt-4o", at)
		if !known || p.InputPer1M != money.MustParseUSD("2.50") {
			t.Errorf("after a failed reload the book lost its prices: %s (known=%v)", p.InputPer1M, known)
		}
	})
}

func TestProvidersAndModels(t *testing.T) {
	t.Parallel()

	b := loadTestBook(t, map[string]string{"base.yaml": basicBook}, Options{})
	at := date(2026, time.July, 1)

	if got := b.Providers(); len(got) != 2 || got[0] != "anthropic" || got[1] != "openai" {
		t.Errorf("Providers() = %v, want [anthropic openai]", got)
	}

	models := b.Models("openai", at)
	if len(models) != 2 || models[0] != "gpt-4o" || models[1] != "gpt-4o-mini" {
		t.Errorf("Models(openai) = %v", models)
	}
	if got := b.Models("nobody", at); len(got) != 0 {
		t.Errorf("Models(nobody) = %v, want empty", got)
	}
}

func TestLoadedAt(t *testing.T) {
	t.Parallel()

	b := loadTestBook(t, map[string]string{"base.yaml": basicBook}, Options{})
	if b.LoadedAt().IsZero() {
		t.Error("LoadedAt is zero after a successful load")
	}
}

func TestMetadataIdentifiesOnlyActivePrices(t *testing.T) {
	t.Parallel()

	future := `
version: 1
effective: 2027-01-01
providers:
  openai:
    source: https://example.invalid/openai
    models:
      future-model:
        input_per_1m: 1.00
        output_per_1m: 2.00
        default_max_tokens: 100
`
	b := loadTestBook(t, map[string]string{"base.yaml": basicBook, "future.yaml": future}, Options{})
	current := b.Metadata(date(2026, time.July, 1))
	if len(current.Revision) != 64 || current.LoadedAt.IsZero() ||
		!current.LatestEffective.Equal(date(2026, time.January, 1)) || current.Providers != 2 || current.Models != 4 {
		t.Fatalf("current metadata = %+v", current)
	}
	later := b.Metadata(date(2027, time.January, 2))
	if later.Revision == current.Revision || later.Models != 5 ||
		!later.LatestEffective.Equal(date(2027, time.January, 1)) {
		t.Fatalf("future metadata = %+v", later)
	}
}
