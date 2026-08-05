// Package reconcile compares the immutable spendlease ledger with a
// provider-neutral vendor statement.
package reconcile

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/billing"
	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
)

// StatementEntry is one row from a normalized vendor billing export.
type StatementEntry struct {
	Provider   string
	Model      string
	ExternalID string
	Usage      billing.Usage
	Cost       money.Nanos
	OccurredAt time.Time
}

// Options selects a half-open accounting interval and its cost tolerance.
type Options struct {
	Since         time.Time
	Until         time.Time
	CostTolerance money.Nanos
}

// Side is one aggregate being compared.
type Side struct {
	Records     int           `json:"records"`
	CostUSD     string        `json:"cost_usd"`
	Usage       billing.Usage `json:"usage"`
	ExternalIDs int           `json:"external_ids"`
}

// Group compares one provider/model pair.
type Group struct {
	Provider           string           `json:"provider"`
	Model              string           `json:"model"`
	Status             string           `json:"status"`
	Ledger             Side             `json:"ledger"`
	Statement          Side             `json:"statement"`
	CostDeltaUSD       string           `json:"cost_delta_usd"`
	UsageDelta         map[string]int64 `json:"usage_delta"`
	MatchedExternalIDs int              `json:"matched_external_ids"`
}

// Report is the stable, versioned reconciliation result.
type Report struct {
	Version          int       `json:"version"`
	Since            time.Time `json:"since"`
	Until            time.Time `json:"until"`
	CostToleranceUSD string    `json:"cost_tolerance_usd"`
	Status           string    `json:"status"`
	Groups           []Group   `json:"groups"`
}

// Mismatched reports whether any group needs investigation.
func (r Report) Mismatched() bool { return r.Status != "match" }

type aggregate struct {
	records int
	cost    money.Nanos
	usage   billing.Usage
	ids     map[string]struct{}
}

type groupKey struct{ provider, model string }

// Build aggregates both sources and compares the named billable dimensions.
func Build(entries []ledger.Entry, statement []StatementEntry, opts Options) (Report, error) {
	if opts.Since.IsZero() || opts.Until.IsZero() || !opts.Since.Before(opts.Until) {
		return Report{}, fmt.Errorf("reconcile: --since must be before --until")
	}
	if opts.CostTolerance < 0 {
		return Report{}, fmt.Errorf("reconcile: cost tolerance cannot be negative")
	}

	ledgerGroups := map[groupKey]*aggregate{}
	statementGroups := map[groupKey]*aggregate{}
	for _, entry := range entries {
		if !inPeriod(entry.CreatedAt, opts) {
			continue
		}
		if err := add(ledgerGroups, groupKey{entry.Provider, entry.Model}, entry.ItemizedUsage(), entry.Cost, entry.ExternalID); err != nil {
			return Report{}, err
		}
	}
	for _, entry := range statement {
		if !inPeriod(entry.OccurredAt, opts) {
			continue
		}
		if err := add(statementGroups, groupKey{entry.Provider, entry.Model}, entry.Usage, entry.Cost, entry.ExternalID); err != nil {
			return Report{}, err
		}
	}

	keys := make([]groupKey, 0, len(ledgerGroups)+len(statementGroups))
	seen := map[groupKey]bool{}
	for key := range ledgerGroups {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range statementGroups {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider == keys[j].provider {
			return keys[i].model < keys[j].model
		}
		return keys[i].provider < keys[j].provider
	})

	report := Report{
		Version: 1, Since: opts.Since.UTC(), Until: opts.Until.UTC(),
		CostToleranceUSD: opts.CostTolerance.String(), Status: "match",
	}
	for _, key := range keys {
		left := value(ledgerGroups[key])
		right := value(statementGroups[key])
		deltaCost, err := subtractNanos(right.cost, left.cost)
		if err != nil {
			return Report{}, fmt.Errorf("reconcile: cost delta overflows for %s/%s", key.provider, key.model)
		}
		deltaUsage, err := usageDelta(left.usage, right.usage)
		if err != nil {
			return Report{}, fmt.Errorf("reconcile: %w for %s/%s", err, key.provider, key.model)
		}
		status := groupStatus(left, right, deltaCost, deltaUsage, opts.CostTolerance)
		if status != "match" {
			report.Status = "mismatch"
		}
		report.Groups = append(report.Groups, Group{
			Provider: key.provider, Model: key.model, Status: status,
			Ledger: side(left), Statement: side(right), CostDeltaUSD: deltaCost.String(),
			UsageDelta: deltaUsage, MatchedExternalIDs: intersection(left.ids, right.ids),
		})
	}
	return report, nil
}

func inPeriod(at time.Time, opts Options) bool {
	at = at.UTC()
	return !at.Before(opts.Since.UTC()) && at.Before(opts.Until.UTC())
}

func add(groups map[groupKey]*aggregate, key groupKey, usage billing.Usage, cost money.Nanos, externalID string) error {
	if strings.TrimSpace(key.provider) == "" || strings.TrimSpace(key.model) == "" {
		return fmt.Errorf("reconcile: provider and model are required")
	}
	if err := usage.Validate(); err != nil {
		return err
	}
	if cost < 0 {
		return fmt.Errorf("reconcile: negative costs require an explicit compensating ledger entry")
	}
	agg := groups[key]
	if agg == nil {
		agg = &aggregate{usage: billing.Usage{}, ids: map[string]struct{}{}}
		groups[key] = agg
	}
	if int64(agg.cost) > math.MaxInt64-int64(cost) {
		return fmt.Errorf("reconcile: cost total overflows for %s/%s", key.provider, key.model)
	}
	agg.cost += cost
	agg.records++
	for unit, quantity := range usage {
		if agg.usage[unit] > math.MaxInt64-quantity {
			return fmt.Errorf("reconcile: usage total overflows for %s/%s unit %s", key.provider, key.model, unit)
		}
		agg.usage[unit] += quantity
	}
	if externalID = strings.TrimSpace(externalID); externalID != "" {
		agg.ids[externalID] = struct{}{}
	}
	return nil
}

func value(a *aggregate) aggregate {
	if a == nil {
		return aggregate{usage: billing.Usage{}, ids: map[string]struct{}{}}
	}
	return *a
}

func side(a aggregate) Side {
	return Side{Records: a.records, CostUSD: a.cost.String(), Usage: a.usage.Normalized(), ExternalIDs: len(a.ids)}
}

func usageDelta(ledgerUsage, statementUsage billing.Usage) (map[string]int64, error) {
	out := map[string]int64{}
	for unit, quantity := range ledgerUsage {
		out[unit] = -quantity
	}
	for unit, quantity := range statementUsage {
		if out[unit] > math.MaxInt64-quantity {
			return nil, fmt.Errorf("usage delta overflows for unit %s", unit)
		}
		out[unit] += quantity
	}
	for unit, quantity := range out {
		if quantity == 0 {
			delete(out, unit)
		}
	}
	return out, nil
}

func subtractNanos(left, right money.Nanos) (money.Nanos, error) {
	if right > 0 && left < money.Nanos(math.MinInt64)+right {
		return 0, fmt.Errorf("underflow")
	}
	if right < 0 && left > money.Nanos(math.MaxInt64)+right {
		return 0, fmt.Errorf("overflow")
	}
	return left - right, nil
}

func groupStatus(left, right aggregate, costDelta money.Nanos, usageDelta map[string]int64, tolerance money.Nanos) string {
	switch {
	case left.records == 0:
		return "statement_only"
	case right.records == 0:
		return "ledger_only"
	case abs(costDelta) > tolerance:
		return "cost_mismatch"
	case len(usageDelta) > 0:
		return "usage_mismatch"
	default:
		return "match"
	}
}

func abs(value money.Nanos) money.Nanos {
	if value < 0 {
		return -value
	}
	return value
}

func intersection(left, right map[string]struct{}) int {
	count := 0
	for id := range left {
		if _, ok := right[id]; ok {
			count++
		}
	}
	return count
}

// ReadStatementCSV reads the documented provider-neutral statement format.
func ReadStatementCSV(r io.Reader) ([]StatementEntry, error) {
	cr := csv.NewReader(r)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reconcile: reading statement header: %w", err)
	}
	columns := map[string]int{}
	for i, name := range header {
		name = strings.TrimSpace(strings.ToLower(name))
		if _, duplicate := columns[name]; duplicate {
			return nil, fmt.Errorf("reconcile: duplicate statement column %q", name)
		}
		columns[name] = i
	}
	for _, required := range []string{"provider", "model", "usage_json", "cost_usd", "occurred_at"} {
		if _, ok := columns[required]; !ok {
			return nil, fmt.Errorf("reconcile: statement has no %q column", required)
		}
	}

	var out []StatementEntry
	for line := 2; ; line++ {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reconcile: reading statement row %d: %w", line, err)
		}
		get := func(name string) string {
			index, ok := columns[name]
			if !ok || index >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[index])
		}
		usage, err := billing.ParseUsageJSON(get("usage_json"))
		if err != nil {
			return nil, fmt.Errorf("reconcile: statement row %d: %w", line, err)
		}
		cost, err := money.ParseUSD(get("cost_usd"))
		if err != nil || cost < 0 {
			return nil, fmt.Errorf("reconcile: statement row %d has invalid non-negative cost_usd %q", line, get("cost_usd"))
		}
		occurred, err := time.Parse(time.RFC3339Nano, get("occurred_at"))
		if err != nil {
			return nil, fmt.Errorf("reconcile: statement row %d has invalid occurred_at: %w", line, err)
		}
		entry := StatementEntry{
			Provider: get("provider"), Model: get("model"), ExternalID: get("external_id"),
			Usage: usage, Cost: cost, OccurredAt: occurred.UTC(),
		}
		if entry.Provider == "" || entry.Model == "" {
			return nil, fmt.Errorf("reconcile: statement row %d requires provider and model", line)
		}
		out = append(out, entry)
	}
	return out, nil
}

// WriteJSON writes the stable report schema.
func WriteJSON(w io.Writer, report Report) error { return json.NewEncoder(w).Encode(report) }

// WriteCSV writes one row per provider/model aggregate. Usage maps remain JSON
// objects so adding a new billable unit does not change the CSV header.
func WriteCSV(w io.Writer, report Report) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"provider", "model", "status", "ledger_records", "statement_records", "ledger_cost_usd", "statement_cost_usd", "cost_delta_usd", "ledger_usage_json", "statement_usage_json", "usage_delta_json", "ledger_external_ids", "statement_external_ids", "matched_external_ids"}); err != nil {
		return err
	}
	for _, group := range report.Groups {
		leftUsage, _ := group.Ledger.Usage.CanonicalJSON()
		rightUsage, _ := group.Statement.Usage.CanonicalJSON()
		deltaUsage, err := json.Marshal(group.UsageDelta)
		if err != nil {
			return err
		}
		if err := cw.Write([]string{
			group.Provider, group.Model, group.Status,
			strconv.Itoa(group.Ledger.Records), strconv.Itoa(group.Statement.Records),
			group.Ledger.CostUSD, group.Statement.CostUSD, group.CostDeltaUSD,
			leftUsage, rightUsage, string(deltaUsage),
			strconv.Itoa(group.Ledger.ExternalIDs), strconv.Itoa(group.Statement.ExternalIDs),
			strconv.Itoa(group.MatchedExternalIDs),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
