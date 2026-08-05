package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/reconcile"
	"github.com/premhiru/spendlease/internal/store"
)

func runLedger(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected verify or export", errUsage)
	}
	switch args[0] {
	case "help", "-h", "--help":
		ledgerUsage(stdout)
		return nil
	case "verify":
		return runLedgerVerify(args[1:], stdout, stderr)
	case "export":
		return runLedgerExport(args[1:], stdout, stderr)
	case "reconcile":
		return runLedgerReconcile(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("%w: unknown ledger subcommand %q", errUsage, args[0])
	}
}

func ledgerUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  spendlease ledger verify [--store PATH]
  spendlease ledger export [--store PATH] [--format json|csv]
                           [--run RUN_ID] [--principal PRINCIPAL_ID]
                           [--since RFC3339]
  spendlease ledger reconcile --statement FILE --since RFC3339 --until RFC3339
                              [--store PATH] [--format json|csv]
                              [--cost-tolerance USD] [--fail-on-mismatch]
`)
}

func runLedgerReconcile(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ledger reconcile", stderr)
	path := storeFlag(fs)
	statementPath := fs.String("statement", "", "normalized vendor statement CSV")
	sinceRaw := fs.String("since", "", "inclusive RFC 3339 period start")
	untilRaw := fs.String("until", "", "exclusive RFC 3339 period end")
	format := fs.String("format", "json", "json or csv")
	toleranceRaw := fs.String("cost-tolerance", "0.01", "allowed absolute USD difference per provider/model")
	failOnMismatch := fs.Bool("fail-on-mismatch", false, "exit non-zero when any group differs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*statementPath) == "" {
		return fmt.Errorf("%w: --statement is required", errUsage)
	}
	since, err := parseReconcileTime("--since", *sinceRaw)
	if err != nil {
		return err
	}
	until, err := parseReconcileTime("--until", *untilRaw)
	if err != nil {
		return err
	}
	tolerance, err := money.ParseUSD(*toleranceRaw)
	if err != nil || tolerance < 0 {
		return fmt.Errorf("%w: --cost-tolerance must be a non-negative USD amount", errUsage)
	}

	statementFile, err := os.Open(*statementPath)
	if err != nil {
		return fmt.Errorf("opening vendor statement: %w", err)
	}
	statement, readErr := reconcile.ReadStatementCSV(statementFile)
	closeErr := statementFile.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return fmt.Errorf("closing vendor statement: %w", closeErr)
	}

	ctx := context.Background()
	st, err := openStore(ctx, *path, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	entries, err := st.LedgerEntries(ctx, store.LedgerFilter{Since: since})
	if err != nil {
		return err
	}
	report, err := reconcile.Build(entries, statement, reconcile.Options{
		Since: since, Until: until, CostTolerance: tolerance,
	})
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "json":
		err = reconcile.WriteJSON(stdout, report)
	case "csv":
		err = reconcile.WriteCSV(stdout, report)
	default:
		return fmt.Errorf("%w: --format must be json or csv", errUsage)
	}
	if err != nil {
		return err
	}
	if report.Mismatched() && *failOnMismatch {
		return fmt.Errorf("reconciliation found differences outside the configured tolerance")
	}
	return nil
}

func parseReconcileTime(flagName, raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("%w: %s is required", errUsage, flagName)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s must be an RFC 3339 timestamp", errUsage, flagName)
	}
	return parsed.UTC(), nil
}

func runLedgerVerify(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ledger verify", stderr)
	path := storeFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	st, err := openStore(ctx, *path, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	entries, err := st.LedgerEntries(ctx, store.LedgerFilter{})
	if err != nil {
		return err
	}
	if err := ledger.VerifyChain(entries); err != nil {
		return err
	}
	head := ledger.GenesisHash
	if len(entries) > 0 {
		head = entries[len(entries)-1].Hash
	}
	fmt.Fprintf(stdout, "Ledger verified: %d entries, head %s\n", len(entries), head)
	return nil
}

func runLedgerExport(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ledger export", stderr)
	path := storeFlag(fs)
	format := fs.String("format", "json", "json or csv")
	runID := fs.String("run", "", "limit to one run ID")
	principalID := fs.String("principal", "", "limit to one principal ID")
	sinceRaw := fs.String("since", "", "limit to entries at or after an RFC 3339 timestamp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	filter := store.LedgerFilter{RunID: strings.TrimSpace(*runID), PrincipalID: strings.TrimSpace(*principalID)}
	if raw := strings.TrimSpace(*sinceRaw); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return fmt.Errorf("%w: --since must be an RFC 3339 timestamp", errUsage)
		}
		filter.Since = since
	}
	ctx := context.Background()
	st, err := openStore(ctx, *path, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	entries, err := st.LedgerEntries(ctx, filter)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "json":
		return ledger.WriteJSON(stdout, entries)
	case "csv":
		return ledger.WriteCSV(stdout, entries)
	default:
		return fmt.Errorf("%w: --format must be json or csv", errUsage)
	}
}
