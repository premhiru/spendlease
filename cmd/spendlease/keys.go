package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
	"github.com/premhiru/spendlease/internal/vault"
)

// runKeys dispatches the key-management subcommands.
//
// These commands manage the local credential vault and authorization objects.
func runKeys(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected one of principal, provider, master, run, lease, revoke", errUsage)
	}

	switch args[0] {
	case "principal":
		return runKeysPrincipal(args[1:], stdout, stderr)
	case "provider":
		return runKeysProvider(args[1:], stdout, stderr)
	case "master":
		return runKeysMaster(args[1:], stdout, stderr)
	case "run":
		return runKeysRun(args[1:], stdout, stderr)
	case "lease":
		return runKeysLease(args[1:], stdout, stderr)
	case "revoke":
		return runKeysRevoke(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
	}
}

// openStore opens the datastore for a CLI command.
func openStore(ctx context.Context, path string, stderr io.Writer) (*sqlite.Store, error) {
	// CLI commands should be quiet unless something is wrong.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return sqlite.Open(ctx, path, sqlite.Options{Logger: logger})
}

// runKeysPrincipal handles `keys principal ...`.
func runKeysPrincipal(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected one of create, list, set-mode", errUsage)
	}
	action, rest := args[0], args[1:]

	fs := newFlagSet("keys principal "+action, stderr)
	storePath := fs.String("store", "./spendlease.db", "SQLite file path")
	name := fs.String("name", "", "principal name")
	mode := fs.String("mode", string(store.ModeObserve), "observe or enforce")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	ctx := context.Background()
	st, err := openStore(ctx, *storePath, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	switch action {
	case "create":
		if *name == "" {
			return fmt.Errorf("%w: --name is required", errUsage)
		}
		m := store.Mode(*mode)
		if !m.Valid() {
			return fmt.Errorf("%w: mode %q is not observe or enforce", errUsage, *mode)
		}

		key, hash := store.NewPrincipalKey()
		p := store.Principal{
			ID: store.NewPrincipalID(), Name: *name,
			KeyHash: hash, Mode: m, CreatedAt: time.Now(),
		}
		if err := st.CreatePrincipal(ctx, p); err != nil {
			return err
		}

		fmt.Fprintf(stdout, "Created principal %s (%s), mode %s\n\n", p.Name, p.ID, p.Mode)
		fmt.Fprintf(stdout, "  %s\n\n", key)
		fmt.Fprintln(stdout, "This key is shown once and is not recoverable. Store it now.")
		if m == store.ModeObserve {
			fmt.Fprintln(stdout, "The principal starts in observe mode: everything is recorded, nothing is blocked.")
		}
		return nil

	case "list":
		ps, err := st.ListPrincipals(ctx)
		if err != nil {
			return err
		}
		if len(ps) == 0 {
			fmt.Fprintln(stdout, "No principals yet. Create one with:")
			fmt.Fprintln(stdout, "  spendlease keys principal create --name my-agent")
			return nil
		}
		fmt.Fprintf(stdout, "%-32s %-24s %s\n", "ID", "NAME", "MODE")
		for _, p := range ps {
			fmt.Fprintf(stdout, "%-32s %-24s %s\n", p.ID, p.Name, p.Mode)
		}
		return nil

	case "set-mode":
		if *name == "" {
			return fmt.Errorf("%w: --name is required", errUsage)
		}
		m := store.Mode(*mode)
		if !m.Valid() {
			return fmt.Errorf("%w: mode %q is not observe or enforce", errUsage, *mode)
		}
		ps, err := st.ListPrincipals(ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			if p.Name == *name {
				if err := st.SetPrincipalMode(ctx, p.ID, m); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "%s is now in %s mode\n", p.Name, m)
				return nil
			}
		}
		return fmt.Errorf("no principal named %q", *name)

	default:
		return fmt.Errorf("%w: unknown subcommand %q", errUsage, action)
	}
}

// runKeysProvider handles `keys provider ...`, the vendor credential vault.
func runKeysProvider(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected one of set, list, rm", errUsage)
	}
	action, rest := args[0], args[1:]

	// Go's flag package stops parsing at the first non-flag argument, so
	// `set openai --key sk-...` would leave --key unparsed. Lifting the
	// provider name out first makes the natural word order work, which is the
	// order every example and error message tells people to use.
	provider, rest := takePositional(rest)

	fs := newFlagSet("keys provider "+action, stderr)
	storePath := fs.String("store", "./spendlease.db", "SQLite file path")
	apiKey := fs.String("key", "", "the vendor API key (omit to read from stdin)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if provider == "" {
		provider = fs.Arg(0)
	}

	ctx := context.Background()
	st, err := openStore(ctx, *storePath, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	masterKey, _, err := resolveMasterKey(*storePath)
	if err != nil {
		return err
	}
	v, err := vault.New(masterKey, st)
	if err != nil {
		return err
	}

	switch action {
	case "set":
		if provider == "" {
			return fmt.Errorf("%w: expected a provider name, for example `keys provider set openai --key sk-...`", errUsage)
		}

		secret := *apiKey
		if secret == "" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading the key from stdin: %w", err)
			}
			secret = trimSpace(string(b))
		}
		if secret == "" {
			return fmt.Errorf("%w: no key given; pass --key or pipe it on stdin", errUsage)
		}

		if err := v.Put(ctx, provider, secret); err != nil {
			return err
		}
		// The key itself is never echoed back.
		fmt.Fprintf(stdout, "Stored the %s API key, encrypted at rest.\n", provider)
		return nil

	case "list":
		names, err := v.Providers(ctx)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No vendor keys stored. Add one with:")
			fmt.Fprintln(stdout, "  spendlease keys provider set openai --key sk-...")
			return nil
		}
		for _, n := range names {
			fmt.Fprintf(stdout, "%s\n", n)
		}
		return nil

	case "rm":
		if provider == "" {
			return fmt.Errorf("%w: expected a provider name", errUsage)
		}
		if err := v.Delete(ctx, provider); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed the %s API key.\n", provider)
		return nil

	default:
		return fmt.Errorf("%w: unknown subcommand %q", errUsage, action)
	}
}

// runKeysMaster handles `keys master ...`.
func runKeysMaster(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "generate" {
		return fmt.Errorf("%w: expected `keys master generate`", errUsage)
	}

	fs := newFlagSet("keys master generate", stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	k, err := vault.GenerateMasterKey()
	if err != nil {
		return err
	}

	// Printed to stdout so it can be piped into a secret manager, with the
	// guidance on stderr so it does not contaminate that pipe.
	fmt.Fprintln(stdout, k.Hex())
	fmt.Fprintf(stderr,
		"\nStore this in your secret manager and provide it as %s.\n"+
			"Every vendor credential is encrypted under it. If it is lost, those\n"+
			"credentials cannot be recovered and must be re-entered.\n",
		EnvMasterKey)
	return nil
}

// takePositional lifts a leading non-flag argument out of args.
//
// It exists because Go's flag package stops parsing at the first positional,
// so `provider set openai --key sk-...` would silently ignore --key. Rather
// than forcing an unnatural `--key sk-... openai`, the positional is removed
// before parsing and the remaining flags parse normally.
//
// A bare "-" or "--" is left alone: those are flag syntax, not a name.
func takePositional(args []string) (positional string, rest []string) {
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// trimSpace trims ASCII whitespace, including the trailing newline a shell
// pipeline adds.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
