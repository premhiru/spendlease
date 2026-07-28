package money

import (
	"errors"
	"strings"
	"testing"
)

func TestParseUSD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    Nanos
		wantErr bool
	}{
		{name: "whole dollars", in: "25.00", want: 25 * Dollar},
		{name: "integer with no point", in: "25", want: 25 * Dollar},
		{name: "zero", in: "0", want: 0},
		{name: "zero with decimals", in: "0.00", want: 0},
		{name: "cents", in: "0.01", want: Cent},
		{name: "one nanodollar", in: "0.000000001", want: Nano},
		{name: "gpt-4o input token price", in: "0.0000025", want: 2500 * Nano},
		{name: "leading dot", in: ".5", want: 500 * Milli},
		{name: "dollar sign is tolerated", in: "$25.00", want: 25 * Dollar},
		{name: "whitespace is tolerated", in: "  25.00  ", want: 25 * Dollar},
		{name: "explicit plus", in: "+3.50", want: 350 * Cent},
		{name: "negative", in: "-1.5", want: -1500 * Milli},
		{name: "negative zero is zero", in: "-0.00", want: 0},
		{name: "trailing zeros do not change value", in: "1.500000000", want: 1500 * Milli},
		{name: "large but in range", in: "9223372035.000000000", want: Nanos(9_223_372_035_000_000_000)},

		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "sign with no digits", in: "-", wantErr: true},
		{name: "point with no fraction", in: "1.", wantErr: true},
		{name: "letters", in: "abc", wantErr: true},
		{name: "trailing letters", in: "1.0x", wantErr: true},
		{name: "two points", in: "1.2.3", wantErr: true},
		{name: "ten decimal places is rejected not rounded", in: "0.0000000001", wantErr: true},
		{name: "underscores are not accepted", in: "1_000.00", wantErr: true},
		{name: "scientific notation is not accepted", in: "1e3", wantErr: true},
		{name: "overflows int64", in: "99999999999.00", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseUSD(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseUSD(%q) = %d, want an error", tt.in, got)
				}
				if !errors.Is(err, ErrInvalidAmount) {
					t.Errorf("ParseUSD(%q) error = %v, want it to wrap ErrInvalidAmount", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUSD(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseUSD(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestNanosString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   Nanos
		want string
	}{
		{name: "zero keeps two places", in: 0, want: "0.00"},
		{name: "whole dollars", in: 25 * Dollar, want: "25.00"},
		{name: "cents", in: 1250 * Cent, want: "12.50"},
		{name: "one cent", in: Cent, want: "0.01"},
		{name: "sub-cent keeps precision", in: 2500 * Nano, want: "0.0000025"},
		{name: "one nanodollar", in: Nano, want: "0.000000001"},
		{name: "negative", in: -1500 * Milli, want: "-1.50"},
		{name: "large", in: Nanos(9_223_372_035_000_000_000), want: "9223372035.00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.in.String(); got != tt.want {
				t.Errorf("Nanos(%d).String() = %q, want %q", int64(tt.in), got, tt.want)
			}
		})
	}
}

// TestRoundTrip is the property that matters: anything String produces must
// parse back to the identical amount, or amounts drift every time they pass
// through the API or the dashboard.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	amounts := []Nanos{
		0, Nano, Micro, Milli, Cent, Dollar,
		25 * Dollar, 2500 * Nano, -1, -25 * Dollar,
		1234567890123, -1234567890123,
		Nanos(9_223_372_035_000_000_000),
	}

	for _, want := range amounts {
		s := want.String()
		got, err := ParseUSD(s)
		if err != nil {
			t.Errorf("ParseUSD(%q) from Nanos(%d) failed: %v", s, int64(want), err)
			continue
		}
		if got != want {
			t.Errorf("round trip of %d via %q gave %d", int64(want), s, int64(got))
		}
	}
}

// TestNoFloatDrift is the whole reason this package exists. Summing a price
// that is not representable in binary floating point must stay exact.
func TestNoFloatDrift(t *testing.T) {
	t.Parallel()

	// 0.1 has no exact binary float representation. Ten of them must be
	// exactly one dollar, which float64 arithmetic does not guarantee.
	ten := MustParseUSD("0.10")
	var sum Nanos
	for i := 0; i < 10; i++ {
		sum += ten
	}
	if sum != Dollar {
		t.Errorf("10 x $0.10 = %s, want 1.00", sum)
	}

	// A million gpt-4o input tokens at $2.50/1M must be exactly $2.50.
	perToken := MustParseUSD("0.0000025")
	if got := perToken * 1_000_000; got != MustParseUSD("2.50") {
		t.Errorf("1M tokens at %s = %s, want 2.50", perToken, got)
	}
}

func TestMustParseUSDPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustParseUSD did not panic on invalid input")
		}
		if err, ok := r.(error); !ok || !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("panic value = %v, want it to wrap ErrInvalidAmount", r)
		}
	}()

	MustParseUSD("not money")
}

func TestUSDIsDisplayOnly(t *testing.T) {
	t.Parallel()

	if got := (25 * Dollar).USD(); got != 25.0 {
		t.Errorf("USD() = %v, want 25", got)
	}
	if got := Nanos(0).USD(); got != 0 {
		t.Errorf("USD() = %v, want 0", got)
	}
}

func TestIsZero(t *testing.T) {
	t.Parallel()

	if !Nanos(0).IsZero() {
		t.Error("Nanos(0).IsZero() = false")
	}
	if Nano.IsZero() {
		t.Error("Nano.IsZero() = true")
	}
}

// TestErrorsAreActionable guards the promise that a bad amount tells the
// operator what was wrong with it, not just that something was.
func TestErrorsAreActionable(t *testing.T) {
	t.Parallel()

	_, err := ParseUSD("0.0000000001")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "0.0000000001") {
		t.Errorf("error %q does not quote the offending input", err)
	}
	if !strings.Contains(err.Error(), "nanodollar") {
		t.Errorf("error %q does not say what the limit is", err)
	}
}
