// Command spendlease is the single binary entrypoint for the spendlease spend
// authorization gateway.
//
// It exposes four subcommands:
//
//	serve     run the gateway and dashboard
//	demo      run a simulated agent fleet against a mock provider
//	keys      manage principals, runs and leases
//	version   print version information
//
// Run "spendlease <command> -h" for the flags of an individual command.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
)

// Build information, injected at link time with -ldflags. See the Makefile.
var (
	// version is the semantic version of this build, or "dev" for local builds.
	version = "dev"
	// commit is the git SHA this binary was built from.
	commit = "none"
	// date is the RFC 3339 build timestamp.
	date = "unknown"
)

// errUsage signals that the user invoked the CLI incorrectly. It is reported
// with the usage text and exit code 2, distinguishing "you typed it wrong"
// from "the command ran and failed".
var errUsage = errors.New("usage")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI with the given arguments and streams, returning the
// process exit code. Keeping the body out of main and threading the streams
// through as parameters is what makes the command table testable.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "serve":
		err = runServe(rest, stdout, stderr)
	case "demo":
		err = runDemo(rest, stdout, stderr)
	case "keys":
		err = runKeys(rest, stdout, stderr)
	case "version":
		err = runVersion(rest, stdout)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "spendlease: unknown command %q\n\n", cmd)
		usage(stderr)
		return 2
	}

	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(stderr, "spendlease %s: %v\n", cmd, err)
		return 2
	default:
		fmt.Fprintf(stderr, "spendlease %s: %v\n", cmd, err)
		return 1
	}
}

// usage writes the top-level help text.
func usage(w io.Writer) {
	fmt.Fprint(w, `spendlease - spend authorization gateway for AI agents

Usage:
  spendlease <command> [flags]

Commands:
  serve     Run the gateway and dashboard
  demo      Run a simulated agent fleet against a mock provider
  keys      Manage principals, runs and leases
  version   Print version information

Run "spendlease <command> -h" for the flags of an individual command.
Docs: https://premhiru.github.io/spendlease
`)
}

// newFlagSet builds a flag set that reports errors to stderr rather than
// exiting the process, so run can decide the exit code.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// runServe starts the gateway. The listener, store and proxy arrive in later
// phases; this scaffold validates flags and reports what it would do.
func runServe(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	addr := fs.String("addr", ":4000", "address to listen on")
	store := fs.String("store", "./spendlease.db", "SQLite file path or PostgreSQL URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	logger.Info("starting spendlease", "addr", *addr, "store", redactStore(*store), "version", version)

	fmt.Fprintf(stdout, "serve is not implemented yet (scaffold build %s)\n", version)
	return nil
}

// runDemo starts the simulated agent fleet.
func runDemo(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("demo", stderr)
	target := fs.String("target", "http://localhost:4000", "address of the gateway to drive")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "demo is not implemented yet; would drive %s\n", *target)
	return nil
}

// runKeys manages principals, runs and leases.
func runKeys(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: expected one of principal, run, lease, revoke", errUsage)
	}

	switch sub := fs.Arg(0); sub {
	case "principal", "run", "lease", "revoke":
		fmt.Fprintf(stdout, "keys %s is not implemented yet\n", sub)
		return nil
	default:
		return fmt.Errorf("%w: unknown subcommand %q", errUsage, sub)
	}
}

// runVersion prints build and runtime information.
func runVersion(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: version takes no arguments", errUsage)
	}
	fmt.Fprintf(stdout, "spendlease %s\ncommit:  %s\nbuilt:   %s\ngo:      %s %s/%s\n",
		version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return nil
}

// redactStore strips credentials from a datastore DSN so it is safe to log.
// SQLite paths pass through unchanged; PostgreSQL URLs keep everything except
// the userinfo section.
func redactStore(dsn string) string {
	scheme, rest, found := strings.Cut(dsn, "://")
	if !found {
		return dsn
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = "***@" + rest[at+1:]
	}
	return scheme + "://" + rest
}
