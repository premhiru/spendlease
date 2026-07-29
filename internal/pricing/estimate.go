package pricing

import (
	"unicode"
	"unicode/utf8"
)

// DefaultCharsPerToken is the divisor in the chars-per-token heuristic.
//
// Both major vendors document roughly the same rule of thumb: one token is
// about four characters of English text. It is a heuristic and this package
// says so everywhere it is used.
const DefaultCharsPerToken = 4

// Estimator turns text into an approximate token count.
//
// No tokenizer is bundled. A real BPE tokenizer means vendoring a vocabulary
// per model family, keeping those in step with model releases, and paying the
// binary size and load time for every one — for a number that is only used to
// size a reservation that gets settled against actual usage minutes later.
// The heuristic is documented, its error is bounded, and the settle step
// corrects it. See ADR-0008.
type Estimator struct {
	// CharsPerToken overrides the default divisor. Zero means
	// DefaultCharsPerToken.
	CharsPerToken int
	// DenseScriptFactor scales the estimate for scripts where one character
	// is close to one token, such as Chinese, Japanese and Korean. The
	// chars/4 rule is derived from English and under-counts these badly.
	// Zero means the built-in default.
	DenseScriptFactor float64
}

// defaultDenseScriptFactor reflects that CJK text is roughly one token per
// character rather than one per four, so dense characters are weighted up
// towards a 1:1 ratio.
const defaultDenseScriptFactor = 4.0

// Estimate is an approximate token count, carrying how it was produced.
type Estimate struct {
	// Tokens is the estimated count. It is never negative, and it is never
	// zero for non-empty input: a request that costs something must not
	// estimate to nothing.
	Tokens int64
	// Approximate is true whenever a heuristic was used, which is currently
	// always. A reservation built from an approximate estimate should mark
	// its ledger entry estimated.
	Approximate bool
	// Method names the technique, for logs and for the dashboard.
	Method string
}

// EstimateTokens returns an approximate token count for text using the
// default estimator.
func EstimateTokens(text string) Estimate {
	return Estimator{}.Estimate(text)
}

// Estimate returns an approximate token count for text.
//
// The count is deliberately biased upwards rather than downwards. An
// over-estimate reserves slightly too much and is corrected on settle; an
// under-estimate lets a request through that should have been refused, which
// is the failure this product exists to prevent.
func (e Estimator) Estimate(text string) Estimate {
	if text == "" {
		return Estimate{Tokens: 0, Approximate: true, Method: "chars/4"}
	}

	perToken := e.CharsPerToken
	if perToken <= 0 {
		perToken = DefaultCharsPerToken
	}
	dense := e.DenseScriptFactor
	if dense <= 0 {
		dense = defaultDenseScriptFactor
	}

	var plain, denseCount int64
	for _, r := range text {
		if isDenseScript(r) {
			denseCount++
		} else {
			plain++
		}
	}

	// Round up, so a short prompt never estimates to zero tokens.
	tokens := (plain + int64(perToken) - 1) / int64(perToken)
	tokens += int64(float64(denseCount) * dense / float64(perToken))

	if tokens < 1 && utf8.RuneCountInString(text) > 0 {
		tokens = 1
	}

	return Estimate{Tokens: tokens, Approximate: true, Method: "chars/4"}
}

// isDenseScript reports whether a rune belongs to a script where characters
// map to tokens close to one for one.
func isDenseScript(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Thai, r)
}

// ReservationTokens decides how many output tokens to reserve for a request.
//
// If the caller specified max_tokens, that is the ceiling and it is used. If
// not, the model's documented default applies. The result is never unbounded
// and never zero: reserving nothing would let an unlimited completion through,
// and reserving everything would reject every subsequent request.
func ReservationTokens(requestedMaxTokens int64, p Price) int64 {
	if requestedMaxTokens > 0 {
		return requestedMaxTokens
	}
	if p.DefaultMaxTokens > 0 {
		return p.DefaultMaxTokens
	}
	return DefaultFallback.DefaultMaxTokens
}
