package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

func findPrincipal(ctx context.Context, st store.Store, name string) (store.Principal, error) {
	if strings.HasPrefix(name, store.PrincipalPrefix) {
		return st.GetPrincipal(ctx, name)
	}
	ps, err := st.ListPrincipals(ctx)
	if err != nil {
		return store.Principal{}, err
	}
	for _, p := range ps {
		if p.Name == name {
			return p, nil
		}
	}
	return store.Principal{}, fmt.Errorf("no principal named %q", name)
}

func runKeysRun(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("%w: expected `keys run create`", errUsage)
	}
	fs := newFlagSet("keys run create", stderr)
	path := fs.String("store", "./spendlease.db", "SQLite file path")
	principal := fs.String("principal", "", "principal name or ID")
	parent := fs.String("parent", "", "parent run ID")
	budgetRaw := fs.String("budget", "", "run budget in USD")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *principal == "" || *budgetRaw == "" {
		return fmt.Errorf("%w: --principal and --budget are required", errUsage)
	}
	budget, err := money.ParseUSD(*budgetRaw)
	if err != nil || budget < 0 {
		return fmt.Errorf("%w: invalid budget", errUsage)
	}
	ctx := context.Background()
	st, err := openStore(ctx, *path, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	p, err := findPrincipal(ctx, st, *principal)
	if err != nil {
		return err
	}
	r := store.Run{ID: store.NewRunID(), PrincipalID: p.ID, ParentRunID: *parent, Budget: budget, Status: store.RunActive, CreatedAt: time.Now()}
	if err := st.CreateRun(ctx, r); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Created run %s for %s with budget $%s\n", r.ID, p.Name, budget.String())
	return nil
}

func runKeysLease(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "issue" {
		return fmt.Errorf("%w: expected `keys lease issue`", errUsage)
	}
	fs := newFlagSet("keys lease issue", stderr)
	path := fs.String("store", "./spendlease.db", "SQLite file path")
	runID := fs.String("run", "", "run ID")
	ttl := fs.Duration("ttl", 15*time.Minute, "lease lifetime")
	providers := fs.String("providers", "", "comma-separated provider scope")
	ceilingRaw := fs.String("ceiling", "0", "lease ceiling in USD; zero inherits run budget")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *runID == "" || *ttl <= 0 {
		return fmt.Errorf("%w: --run and a positive --ttl are required", errUsage)
	}
	ceiling, err := money.ParseUSD(*ceilingRaw)
	if err != nil || ceiling < 0 {
		return fmt.Errorf("%w: invalid ceiling", errUsage)
	}
	ctx := context.Background()
	st, err := openStore(ctx, *path, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(ctx, *runID)
	if err != nil {
		return err
	}
	if run.Status != store.RunActive {
		return fmt.Errorf("run %s is closed", run.ID)
	}
	token, hash := store.NewLeaseToken()
	var scope []string
	for _, p := range strings.Split(*providers, ",") {
		if p = strings.TrimSpace(p); p != "" {
			scope = append(scope, p)
		}
	}
	l := store.Lease{ID: store.NewLeaseID(), RunID: run.ID, TokenHash: hash, Providers: scope, Ceiling: ceiling, ExpiresAt: time.Now().Add(*ttl), CreatedAt: time.Now()}
	if err := st.CreateLease(ctx, l); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Issued lease %s for run %s (expires %s)\n\n  %s\n\nThis token is shown once and is not recoverable.\n", l.ID, run.ID, l.ExpiresAt.UTC().Format(time.RFC3339), token)
	return nil
}

func runKeysRevoke(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys revoke", stderr)
	path := fs.String("store", "./spendlease.db", "SQLite file path")
	all := fs.Bool("all", false, "revoke every lease")
	principal := fs.String("principal", "", "limit to one principal name or ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*all {
		return fmt.Errorf("%w: --all is required", errUsage)
	}
	ctx := context.Background()
	st, err := openStore(ctx, *path, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	var ps []store.Principal
	if *principal != "" {
		p, err := findPrincipal(ctx, st, *principal)
		if err != nil {
			return err
		}
		ps = []store.Principal{p}
	} else {
		ps, err = st.ListPrincipals(ctx)
		if err != nil {
			return err
		}
	}
	total := 0
	for _, p := range ps {
		n, err := st.RevokeLeasesForPrincipal(ctx, p.ID, time.Now())
		if err != nil {
			return err
		}
		total += n
	}
	fmt.Fprintf(stdout, "Revoked %d lease(s).\n", total)
	return nil
}
