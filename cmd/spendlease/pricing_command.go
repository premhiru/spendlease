package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/premhiru/spendlease/internal/pricing"
)

type pricingEntryOutput struct {
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	InputPer1M           string `json:"input_per_1m_usd"`
	CachedInputPer1M     string `json:"cached_input_per_1m_usd"`
	CacheWrite5mPer1M    string `json:"cache_write_5m_per_1m_usd"`
	CacheWrite1hPer1M    string `json:"cache_write_1h_per_1m_usd"`
	OutputPer1M          string `json:"output_per_1m_usd"`
	LongContextThreshold int64  `json:"long_context_threshold,omitempty"`
	LongInputPer1M       string `json:"long_input_per_1m_usd,omitempty"`
	LongCachedInputPer1M string `json:"long_cached_input_per_1m_usd,omitempty"`
	LongCacheWritePer1M  string `json:"long_cache_write_per_1m_usd,omitempty"`
	LongOutputPer1M      string `json:"long_output_per_1m_usd,omitempty"`
	DefaultMaxTokens     int64  `json:"default_max_tokens"`
	Effective            string `json:"effective"`
	Verified             string `json:"verified,omitempty"`
	Source               string `json:"source"`
	Free                 bool   `json:"free"`
}

type pricingProviderVerification struct {
	Provider         string `json:"provider"`
	Models           int    `json:"models"`
	OldestVerified   string `json:"oldest_verified,omitempty"`
	UnverifiedModels int    `json:"unverified_models"`
	FutureModels     int    `json:"future_verified_models"`
	Stale            bool   `json:"stale"`
}

type pricingVerificationOutput struct {
	Status      string                        `json:"status"`
	AsOf        string                        `json:"as_of"`
	MaximumAge  string                        `json:"maximum_age"`
	Revision    string                        `json:"revision"`
	Providers   []pricingProviderVerification `json:"providers"`
	StaleModels int                           `json:"stale_models"`
}

func runPricing(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		pricingUsage(stderr)
		return fmt.Errorf("%w: expected list, show, or verify", errUsage)
	}
	switch args[0] {
	case "list":
		return runPricingList(args[1:], stdout, stderr)
	case "show":
		return runPricingShow(args[1:], stdout, stderr)
	case "verify":
		return runPricingVerify(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		pricingUsage(stdout)
		return nil
	default:
		pricingUsage(stderr)
		return fmt.Errorf("%w: unknown pricing command %q", errUsage, args[0])
	}
}

func pricingUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  spendlease pricing list [--provider NAME] [--at YYYY-MM-DD] [--json]
  spendlease pricing show [--at YYYY-MM-DD] [--json] PROVIDER/MODEL
  spendlease pricing verify [--max-age 45d] [--at YYYY-MM-DD] [--json]

All commands accept --pricing DIR to inspect an external price book instead of
the copy embedded in the binary. "verify" validates structure during loading,
then fails when any active entry lacks current vendor-review evidence.
`)
}

func runPricingList(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("pricing list", stderr)
	provider := fs.String("provider", "", "only list one provider")
	atRaw := fs.String("at", "", "resolve prices on this UTC date (default: today)")
	asJSON := fs.Bool("json", false, "emit JSON")
	dir := fs.String("pricing", "", "directory of price-book YAML")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: pricing list takes no positional arguments", errUsage)
	}
	at, err := parsePricingDate(*atRaw)
	if err != nil {
		return err
	}
	book, err := loadPricingCommandBook(*dir)
	if err != nil {
		return err
	}
	entries := book.Entries(at)
	wanted := strings.ToLower(strings.TrimSpace(*provider))
	if wanted != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Provider == wanted {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
		if len(entries) == 0 {
			return fmt.Errorf("provider %q has no active price entries", wanted)
		}
	}
	output := make([]pricingEntryOutput, 0, len(entries))
	for _, entry := range entries {
		output = append(output, pricingOutput(entry))
	}
	if *asJSON {
		return writePricingJSON(stdout, output)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tMODEL\tINPUT / 1M\tOUTPUT / 1M\tVERIFIED")
	for _, entry := range output {
		verified := entry.Verified
		if verified == "" {
			verified = "unverified"
		}
		fmt.Fprintf(tw, "%s\t%s\t$%s\t$%s\t%s\n", entry.Provider, entry.Model, entry.InputPer1M, entry.OutputPer1M, verified)
	}
	return tw.Flush()
}

func runPricingShow(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("pricing show", stderr)
	atRaw := fs.String("at", "", "resolve the price on this UTC date (default: today)")
	asJSON := fs.Bool("json", false, "emit JSON")
	dir := fs.String("pricing", "", "directory of price-book YAML")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: pricing show requires PROVIDER/MODEL", errUsage)
	}
	provider, model, ok := strings.Cut(fs.Arg(0), "/")
	provider, model = strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(model)
	if !ok || provider == "" || model == "" {
		return fmt.Errorf("%w: model must be written as PROVIDER/MODEL", errUsage)
	}
	at, err := parsePricingDate(*atRaw)
	if err != nil {
		return err
	}
	book, err := loadPricingCommandBook(*dir)
	if err != nil {
		return err
	}
	entry, known := book.LookupKnown(provider, model, at)
	if !known {
		return fmt.Errorf("no active price for %s/%s on %s", provider, model, at.Format("2006-01-02"))
	}
	output := pricingOutput(entry)
	if *asJSON {
		return writePricingJSON(stdout, output)
	}
	verified := output.Verified
	if verified == "" {
		verified = "unverified"
	}
	fmt.Fprintf(stdout, "Provider:             %s\nModel:                %s\nInput / 1M:           $%s\nCached input / 1M:    $%s\nCache write 5m / 1M: $%s\nCache write 1h / 1M: $%s\nOutput / 1M:          $%s\nDefault max tokens:   %d\nEffective:            %s\nVerified:             %s\nSource:               %s\n",
		output.Provider, output.Model, output.InputPer1M, output.CachedInputPer1M,
		output.CacheWrite5mPer1M, output.CacheWrite1hPer1M, output.OutputPer1M,
		output.DefaultMaxTokens, output.Effective, verified, output.Source)
	if output.LongContextThreshold > 0 {
		fmt.Fprintf(stdout, "Long-context threshold: %d\nLong input / 1M:       $%s\nLong cached / 1M:      $%s\nLong cache write / 1M: $%s\nLong output / 1M:      $%s\n",
			output.LongContextThreshold, output.LongInputPer1M, output.LongCachedInputPer1M,
			output.LongCacheWritePer1M, output.LongOutputPer1M)
	}
	return nil
}

func runPricingVerify(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("pricing verify", stderr)
	defaultMaxAge := fmt.Sprintf("%dd", pricing.DefaultVerificationMaxAgeDays)
	maxAgeRaw := fs.String("max-age", defaultMaxAge, "maximum age of vendor verification evidence")
	atRaw := fs.String("at", "", "verify freshness on this UTC date (default: today)")
	asJSON := fs.Bool("json", false, "emit JSON")
	dir := fs.String("pricing", "", "directory of price-book YAML")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: pricing verify takes no positional arguments", errUsage)
	}
	maxAge, err := parsePricingAge(*maxAgeRaw)
	if err != nil {
		return err
	}
	at, err := parsePricingDate(*atRaw)
	if err != nil {
		return err
	}
	book, err := loadPricingCommandBook(*dir)
	if err != nil {
		return err
	}
	report := buildPricingVerification(book, at, maxAge, *maxAgeRaw)
	if *asJSON {
		if err := writePricingJSON(stdout, report); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "Price book verification: %s\nRevision: %s\nAs of: %s\nMaximum age: %s\n",
			strings.ToUpper(report.Status), report.Revision, report.AsOf, report.MaximumAge)
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PROVIDER\tMODELS\tOLDEST VERIFICATION\tSTATUS")
		for _, provider := range report.Providers {
			verified, status := provider.OldestVerified, "current"
			if verified == "" {
				verified = "missing"
			}
			if provider.Stale {
				status = "stale"
			}
			if provider.UnverifiedModels > 0 {
				status = fmt.Sprintf("stale (%d unverified)", provider.UnverifiedModels)
			} else if provider.FutureModels > 0 {
				status = fmt.Sprintf("invalid (%d future-dated)", provider.FutureModels)
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", provider.Provider, provider.Models, verified, status)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if report.StaleModels > 0 {
		return fmt.Errorf("price book verification is stale for %d active model entries", report.StaleModels)
	}
	return nil
}

func loadPricingCommandBook(dir string) (*pricing.Book, error) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return loadPriceBook(dir, logger)
}

func parsePricingDate(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Now().UTC(), nil
	}
	at, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: -at must be YYYY-MM-DD", errUsage)
	}
	return at.UTC(), nil
}

func parsePricingAge(raw string) (time.Duration, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("%w: -max-age must be a positive duration such as 45d", errUsage)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	age, err := time.ParseDuration(raw)
	if err != nil || age <= 0 {
		return 0, fmt.Errorf("%w: -max-age must be a positive duration such as 45d", errUsage)
	}
	return age, nil
}

func pricingOutput(entry pricing.Price) pricingEntryOutput {
	output := pricingEntryOutput{
		Provider: entry.Provider, Model: entry.Model,
		InputPer1M: entry.InputPer1M.String(), CachedInputPer1M: entry.CachedInputPer1M.String(),
		CacheWrite5mPer1M: entry.CacheWrite5mPer1M.String(), CacheWrite1hPer1M: entry.CacheWrite1hPer1M.String(),
		OutputPer1M: entry.OutputPer1M.String(), DefaultMaxTokens: entry.DefaultMaxTokens,
		Effective: entry.Effective.UTC().Format("2006-01-02"), Source: entry.Source, Free: entry.Free,
		LongContextThreshold: entry.LongContextThreshold,
	}
	if !entry.Verified.IsZero() {
		output.Verified = entry.Verified.UTC().Format("2006-01-02")
	}
	if entry.LongContextThreshold > 0 {
		output.LongInputPer1M = entry.LongInputPer1M.String()
		output.LongCachedInputPer1M = entry.LongCachedInputPer1M.String()
		output.LongCacheWritePer1M = entry.LongCacheWritePer1M.String()
		output.LongOutputPer1M = entry.LongOutputPer1M.String()
	}
	return output
}

func buildPricingVerification(book *pricing.Book, at time.Time, maxAge time.Duration, maxAgeLabel string) pricingVerificationOutput {
	metadata := book.Metadata(at)
	report := pricingVerificationOutput{
		Status: "current", AsOf: at.UTC().Format("2006-01-02"), MaximumAge: maxAgeLabel,
		Revision: metadata.Revision,
	}
	cutoff := at.Add(-maxAge)
	entries := book.Entries(at)
	for _, provider := range book.Providers() {
		state := pricingProviderVerification{Provider: provider}
		for _, entry := range entries {
			if entry.Provider != provider {
				continue
			}
			state.Models++
			stale := entry.Verified.IsZero() || entry.Verified.Before(cutoff) || entry.Verified.After(at)
			if entry.Verified.IsZero() {
				state.UnverifiedModels++
			} else if entry.Verified.After(at) {
				state.FutureModels++
			} else if state.OldestVerified == "" || entry.Verified.Format("2006-01-02") < state.OldestVerified {
				state.OldestVerified = entry.Verified.UTC().Format("2006-01-02")
			}
			if stale {
				state.Stale = true
				report.StaleModels++
			}
		}
		if state.Models > 0 {
			report.Providers = append(report.Providers, state)
		}
	}
	if report.StaleModels > 0 {
		report.Status = "stale"
	}
	return report
}

func writePricingJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
