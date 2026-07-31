package pricing

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		wantMin int64
		wantMax int64
	}{
		{name: "empty", text: "", wantMin: 0, wantMax: 0},
		{name: "a single character never estimates to zero", text: "a", wantMin: 1, wantMax: 1},
		{name: "four characters", text: "abcd", wantMin: 1, wantMax: 1},
		{name: "five characters round up", text: "abcde", wantMin: 2, wantMax: 2},
		{
			name:    "a short english sentence",
			text:    "The quick brown fox jumps over the lazy dog.", // 44 chars
			wantMin: 11, wantMax: 11,
		},
		{
			name:    "a paragraph",
			text:    strings.Repeat("word ", 200), // 1000 chars
			wantMin: 250, wantMax: 250,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := EstimateTokens(tt.text)
			if got.Tokens < tt.wantMin || got.Tokens > tt.wantMax {
				t.Errorf("EstimateTokens(%d chars) = %d, want between %d and %d",
					len(tt.text), got.Tokens, tt.wantMin, tt.wantMax)
			}
			if !got.Approximate {
				t.Error("the estimate is not flagged approximate; there is no tokenizer here")
			}
			if got.Method == "" {
				t.Error("the estimate does not name its method")
			}
		})
	}
}

// TestEstimateNeverZeroForNonEmptyInput guards the rule that a request which
// costs something must not estimate to nothing. A zero estimate would reserve
// zero and let an unbounded call through.
func TestEstimateNeverZeroForNonEmptyInput(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"a", ".", " ", "x", "日", "\n", "é"} {
		if got := EstimateTokens(text); got.Tokens < 1 {
			t.Errorf("EstimateTokens(%q) = %d, want at least 1", text, got.Tokens)
		}
	}
}

// TestDenseScriptsAreNotUnderCounted covers the known weakness of chars/4:
// it is derived from English, and CJK text is closer to one token per
// character. Under-counting there would under-reserve exactly the workloads
// that are most expensive per character.
func TestDenseScriptsAreNotUnderCounted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
	}{
		{name: "chinese", text: strings.Repeat("中文测试", 25)},  // 100 runes
		{name: "japanese", text: strings.Repeat("にほんご", 33)}, // 99 runes
		{name: "korean", text: strings.Repeat("한국어테스트", 20)}, // 120 runes
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runes := int64(len([]rune(tt.text)))
			got := EstimateTokens(tt.text).Tokens

			// Should be far closer to one token per character than to a
			// quarter of one.
			if got < runes/2 {
				t.Errorf("%d runes of %s estimated at %d tokens; chars/4 under-counts dense scripts",
					runes, tt.name, got)
			}
			if got > runes*2 {
				t.Errorf("%d runes of %s estimated at %d tokens, which is wildly high",
					runes, tt.name, got)
			}
		})
	}
}

func TestEstimatorOverrides(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("a", 100)

	base := Estimator{}.Estimate(text).Tokens
	coarse := Estimator{CharsPerToken: 10}.Estimate(text).Tokens

	if base != 25 {
		t.Errorf("default estimate = %d, want 25", base)
	}
	if coarse != 10 {
		t.Errorf("CharsPerToken=10 estimate = %d, want 10", coarse)
	}
	if coarse >= base {
		t.Error("a larger CharsPerToken should produce a smaller estimate")
	}
}

// TestReservationTokens covers the rule that a request without max_tokens
// must never reserve unbounded, and must never reserve zero.
func TestReservationTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		requested int64
		price     Price
		want      int64
	}{
		{
			name:      "the request's own ceiling wins",
			requested: 500, price: Price{DefaultMaxTokens: 4096}, want: 500,
		},
		{
			name:      "a large explicit ceiling is honoured",
			requested: 100_000, price: Price{DefaultMaxTokens: 4096}, want: 100_000,
		},
		{
			name:      "no ceiling falls back to the model default",
			requested: 0, price: Price{DefaultMaxTokens: 8192}, want: 8192,
		},
		{
			name:      "a negative ceiling is ignored",
			requested: -1, price: Price{DefaultMaxTokens: 4096}, want: 4096,
		},
		{
			name:      "no ceiling and no model default still bounds the reservation",
			requested: 0, price: Price{}, want: DefaultFallback.DefaultMaxTokens,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ReservationTokens(tt.requested, tt.price)
			if got != tt.want {
				t.Errorf("ReservationTokens(%d) = %d, want %d", tt.requested, got, tt.want)
			}
			if got <= 0 {
				t.Error("a reservation of zero or fewer tokens would be unbounded in practice")
			}
		})
	}
}

func TestReservationInputTokensUsesAConservativeByteCeiling(t *testing.T) {
	t.Parallel()
	if got := ReservationInputTokens(4096); got != 4096+ReservationInputOverhead {
		t.Fatalf("ReservationInputTokens = %d", got)
	}
	if got := ReservationInputTokens(-1); got != ReservationInputOverhead {
		t.Fatalf("negative byte count produced %d", got)
	}
}
