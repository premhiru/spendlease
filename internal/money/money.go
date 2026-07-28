// Package money represents monetary amounts as exact integers.
//
// Every amount in spendlease is an int64 count of nanodollars, a billionth of
// a US dollar. Floating point is never used: binary floats cannot represent
// 0.1 exactly, and a budget system that disagrees with a vendor invoice about
// the third decimal place is worse than no budget system at all.
//
// Nanodollar precision is not arbitrary. A single gpt-4o input token costs
// $0.0000025, which is 2500 nanodollars but only 2.5 microdollars — so
// microdollar precision would have to round a real per-token price and would
// accumulate error over millions of tokens. See ADR-0003.
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Nanos is an amount of money in nanodollars (1e-9 USD).
//
// The int64 range covers roughly ±$9.2 billion, comfortably more than any
// budget this system is intended to hold.
type Nanos int64

// Common denominations, useful for constructing amounts in code and tests.
const (
	// Nano is the smallest representable amount, 1e-9 USD.
	Nano Nanos = 1
	// Micro is 1e-6 USD, the granularity of most per-token vendor prices.
	Micro Nanos = 1_000
	// Milli is 1e-3 USD, one tenth of a cent.
	Milli Nanos = 1_000_000
	// Cent is 1e-2 USD.
	Cent Nanos = 10_000_000
	// Dollar is 1 USD.
	Dollar Nanos = 1_000_000_000
)

// maxIntegerDollars bounds the whole-dollar part accepted by ParseUSD, so that
// multiplying by Dollar cannot overflow int64.
const maxIntegerDollars = int64(9_223_372_035)

// ErrInvalidAmount is returned when a string is not a well-formed USD amount.
var ErrInvalidAmount = errors.New("money: invalid amount")

// ParseUSD converts a decimal USD string such as "25.00", "-1.5" or
// "0.0000025" into Nanos, exactly and without ever constructing a float.
//
// Up to nine decimal places are accepted, which is full nanodollar precision.
// A tenth decimal place is rejected rather than silently rounded, because a
// price that cannot be represented is a data problem the caller needs to know
// about. A leading "$" and surrounding whitespace are tolerated.
func ParseUSD(s string) (Nanos, error) {
	raw := s
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, fmt.Errorf("%w: %q is empty", ErrInvalidAmount, raw)
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("%w: %q has a sign but no digits", ErrInvalidAmount, raw)
	}

	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0" // ".5" means "0.5"
	}
	if hasFrac && frac == "" {
		return 0, fmt.Errorf("%w: %q has a decimal point but no fraction", ErrInvalidAmount, raw)
	}

	if !allDigits(whole) {
		return 0, fmt.Errorf("%w: %q has a non-numeric whole part", ErrInvalidAmount, raw)
	}
	if hasFrac && !allDigits(frac) {
		return 0, fmt.Errorf("%w: %q has a non-numeric fraction", ErrInvalidAmount, raw)
	}
	if len(frac) > 9 {
		return 0, fmt.Errorf("%w: %q has more than nanodollar precision", ErrInvalidAmount, raw)
	}

	dollars, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || dollars > maxIntegerDollars {
		return 0, fmt.Errorf("%w: %q is out of range", ErrInvalidAmount, raw)
	}

	// Right-pad the fraction to exactly nine digits so it is a nanodollar count.
	frac += strings.Repeat("0", 9-len(frac))
	nanos, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q has an unparseable fraction", ErrInvalidAmount, raw)
	}

	total := dollars*int64(Dollar) + nanos
	if total < 0 {
		return 0, fmt.Errorf("%w: %q overflows", ErrInvalidAmount, raw)
	}
	if neg {
		total = -total
	}
	return Nanos(total), nil
}

// MustParseUSD is ParseUSD that panics on failure. It is for constants and
// tests, never for input that came from outside the process.
func MustParseUSD(s string) Nanos {
	n, err := ParseUSD(s)
	if err != nil {
		panic(err)
	}
	return n
}

// String renders the amount as a plain decimal USD string with no currency
// symbol and no thousands separators, trimming trailing fractional zeros but
// always keeping at least two decimal places: "25.00", "0.0000025".
//
// The result round-trips exactly through ParseUSD.
func (n Nanos) String() string {
	neg := n < 0
	v := int64(n)
	if neg {
		v = -v
	}

	whole := v / int64(Dollar)
	frac := v % int64(Dollar)

	out := strconv.FormatInt(whole, 10)
	digits := fmt.Sprintf("%09d", frac)
	digits = strings.TrimRight(digits, "0")
	for len(digits) < 2 {
		digits += "0"
	}
	out += "." + digits

	if neg {
		out = "-" + out
	}
	return out
}

// USD returns the amount as a float64, for display and for serialising to
// JSON where a human will read it.
//
// Never use the result for arithmetic and never store it. It is lossy by
// construction; that is the entire reason this package exists.
func (n Nanos) USD() float64 {
	return float64(n) / float64(Dollar)
}

// IsZero reports whether the amount is exactly zero.
func (n Nanos) IsZero() bool { return n == 0 }

// allDigits reports whether s is non-empty and contains only ASCII digits.
// strconv.ParseInt would accept forms this package should not, such as "+5"
// nested inside a fraction or underscore separators.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
