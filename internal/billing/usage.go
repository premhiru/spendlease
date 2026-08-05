// Package billing defines provider-neutral billable quantities.
package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	// UnitInputTokens is uncached prompt input.
	UnitInputTokens = "input_tokens"
	// UnitCachedInputTokens is prompt input served from a provider cache.
	UnitCachedInputTokens = "cached_input_tokens"
	// UnitCacheWrite5mTokens is prompt input written to a five-minute cache.
	UnitCacheWrite5mTokens = "cache_write_5m_tokens"
	// UnitCacheWrite1hTokens is prompt input written to a one-hour cache.
	UnitCacheWrite1hTokens = "cache_write_1h_tokens"
	// UnitOutputTokens is generated output.
	UnitOutputTokens = "output_tokens"
)

// Usage maps a stable unit name to its non-negative integer quantity. Unit
// names are intentionally open-ended so invoice-only dimensions can be
// reconciled before the gateway learns how to reserve them safely.
type Usage map[string]int64

// TokenUsage returns the five token dimensions currently priced by the
// gateway. Zero quantities are omitted from the canonical representation.
func TokenUsage(input, cached, write5m, write1h, output int64) Usage {
	return Usage{
		UnitInputTokens:        input,
		UnitCachedInputTokens:  cached,
		UnitCacheWrite5mTokens: write5m,
		UnitCacheWrite1hTokens: write1h,
		UnitOutputTokens:       output,
	}.Normalized()
}

// Normalized returns a copy without zero quantities.
func (u Usage) Normalized() Usage {
	out := make(Usage, len(u))
	for unit, quantity := range u {
		if quantity != 0 {
			out[unit] = quantity
		}
	}
	return out
}

// Validate rejects malformed dimensions before they enter the immutable
// ledger or a reconciliation report.
func (u Usage) Validate() error {
	for unit, quantity := range u {
		if !validUnit(unit) {
			return fmt.Errorf("billing: invalid usage unit %q", unit)
		}
		if quantity < 0 {
			return fmt.Errorf("billing: usage unit %q has negative quantity %d", unit, quantity)
		}
	}
	return nil
}

func validUnit(unit string) bool {
	if unit == "" || len(unit) > 64 || strings.TrimSpace(unit) != unit {
		return false
	}
	for i := 0; i < len(unit); i++ {
		char := unit[i]
		if (char >= 'a' && char <= 'z') || (i > 0 && char >= '0' && char <= '9') ||
			(i > 0 && (char == '_' || char == '.')) {
			continue
		}
		return false
	}
	return true
}

// Add adds every quantity in other to u.
func (u Usage) Add(other Usage) {
	for unit, quantity := range other {
		u[unit] += quantity
	}
}

// CanonicalJSON returns deterministic JSON with lexicographically sorted
// keys. encoding/json currently sorts map keys, but the hash format should not
// depend on an implementation detail of a general-purpose encoder.
func (u Usage) CanonicalJSON() (string, error) {
	if err := u.Validate(); err != nil {
		return "", err
	}
	normal := u.Normalized()
	keys := make([]string, 0, len(normal))
	for unit := range normal {
		keys = append(keys, unit)
	}
	sort.Strings(keys)

	var out bytes.Buffer
	out.WriteByte('{')
	for i, unit := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(unit)
		out.Write(name)
		out.WriteByte(':')
		value, _ := json.Marshal(normal[unit])
		out.Write(value)
	}
	out.WriteByte('}')
	return out.String(), nil
}

// ParseUsageJSON decodes the normalized statement and ledger representation.
// Numbers must be JSON integers; floating point quantities are not silently
// rounded.
func ParseUsageJSON(raw string) (Usage, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var values map[string]json.Number
	if err := dec.Decode(&values); err != nil {
		return nil, fmt.Errorf("billing: parsing usage JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("billing: parsing usage JSON: trailing data")
		}
		return nil, fmt.Errorf("billing: parsing usage JSON: %w", err)
	}
	out := make(Usage, len(values))
	for unit, number := range values {
		quantity, err := number.Int64()
		if err != nil {
			return nil, fmt.Errorf("billing: usage unit %q is not an integer: %w", unit, err)
		}
		out[unit] = quantity
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out.Normalized(), nil
}
