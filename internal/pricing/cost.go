package pricing

import (
	"time"

	"github.com/premhiru/spendlease/internal/money"
)

// tokensPerUnit is the denominator of every published rate: vendors quote
// prices per one million tokens.
const tokensPerUnit = 1_000_000

// Usage is a token count to be priced.
type Usage struct {
	// InputTokens is the prompt size.
	InputTokens int64
	// OutputTokens is the completion size, or the reserved ceiling when the
	// request has not completed yet.
	OutputTokens int64
}

// Cost prices a usage against a resolved price.
//
// The result is exact: no floating point is involved at any step.
func (p Price) Cost(u Usage) money.Nanos {
	return rate(u.InputTokens, p.InputPer1M) + rate(u.OutputTokens, p.OutputPer1M)
}

// InputCost prices only the prompt.
func (p Price) InputCost(tokens int64) money.Nanos { return rate(tokens, p.InputPer1M) }

// OutputCost prices only the completion.
func (p Price) OutputCost(tokens int64) money.Nanos { return rate(tokens, p.OutputPer1M) }

// Cost is the convenience path: resolve a model's price and apply it.
//
// The bool reports whether the model was found in the book. False means the
// fallback was used and any ledger entry built from this must be marked
// estimated.
func (b *Book) Cost(provider, model string, u Usage, at time.Time) (money.Nanos, bool) {
	p, known := b.Lookup(provider, model, at)
	return p.Cost(u), known
}

// rate multiplies a token count by a per-million-token price, exactly.
//
// The naive form, tokens*perMillion/1_000_000, overflows int64 for large
// requests: a million tokens against a $1000/MTok rate is 1e6 * 1e12 = 1e18,
// and only a little more overflows. Splitting the token count into whole
// millions and a remainder keeps every intermediate product small:
//
//	whole*perMillion  is bounded by the total cost itself
//	rem*perMillion    is at most 999_999 * perMillion
//
// The remainder is rounded half-up rather than truncated. Truncation would
// bias every single charge downwards, and a spend limiter that consistently
// under-counts is the wrong kind of wrong.
func rate(tokens int64, perMillion money.Nanos) money.Nanos {
	if tokens <= 0 || perMillion == 0 {
		return 0
	}

	whole := tokens / tokensPerUnit
	rem := tokens % tokensPerUnit

	cost := money.Nanos(whole) * perMillion
	if rem > 0 {
		scaled := money.Nanos(rem) * perMillion
		cost += (scaled + tokensPerUnit/2) / tokensPerUnit
	}
	return cost
}
