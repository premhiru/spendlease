package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/ledger"
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
`)
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
