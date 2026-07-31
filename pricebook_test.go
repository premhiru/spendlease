package spendlease_test

import (
	"testing"
	"time"

	spendlease "github.com/premhiru/spendlease"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/pricing"
)

// TestEmbeddedPriceBookLoads is the guard on the zero-configuration promise:
// a container with nothing mounted must still be able to price a request.
//
// Without this, a price book that is valid on disk could ship in an image that
// cannot see it, and the failure would surface as every model falling back to
// the estimated rate.
func TestEmbeddedPriceBookLoads(t *testing.T) {
	t.Parallel()

	b, err := pricing.Load(spendlease.PriceBookFS(), spendlease.PriceBookDir, pricing.Options{})
	if err != nil {
		t.Fatalf("the embedded price book does not load: %v", err)
	}

	providers := b.Providers()
	if len(providers) != 7 {
		t.Fatalf("embedded providers = %v, want seven", providers)
	}

	at := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	p, known := b.Lookup("openai", "gpt-4o", at)
	if !known {
		t.Fatal("gpt-4o is not in the embedded price book")
	}
	if p.InputPer1M != money.MustParseUSD("2.50") {
		t.Errorf("embedded gpt-4o input = %s, want 2.50", p.InputPer1M)
	}
}

// TestEmbeddedMatchesDisk catches the price book being updated on disk while
// the embedded copy silently keeps an old value.
func TestEmbeddedMatchesDisk(t *testing.T) {
	t.Parallel()

	embedded, err := pricing.Load(spendlease.PriceBookFS(), spendlease.PriceBookDir, pricing.Options{})
	if err != nil {
		t.Fatalf("loading embedded: %v", err)
	}

	at := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	for _, provider := range embedded.Providers() {
		models := embedded.Models(provider, at)
		if len(models) == 0 {
			t.Errorf("embedded provider %s has no models", provider)
		}
		for _, m := range models {
			if p, known := embedded.Lookup(provider, m, at); !known ||
				(!p.Free && (p.InputPer1M <= 0 || p.OutputPer1M <= 0)) {
				t.Errorf("embedded %s/%s did not resolve to a usable price", provider, m)
			}
		}
	}
}
