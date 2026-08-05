// Command spendlease is the single binary entrypoint for the spendlease spend
// authorization gateway.
//
// It exposes five subcommands:
//
//	serve     run the gateway and dashboard
//	demo      run a simulated agent fleet against a mock provider
//	keys      manage principals, runs and leases
//	ledger    verify or export the append-only spend ledger
//	version   print version information
//
// Run "spendlease <command> -h" for the flags of an individual command.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
	case "ledger":
		err = runLedger(rest, stdout, stderr)
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
  serve     Run the gateway
  demo      Run a simulated agent fleet against a mock provider
  keys      Manage principals, operators, credentials, runs, leases, and revocation
  ledger    Verify or export the append-only spend ledger
  version   Print version information

Getting started:
  spendlease keys principal create --name my-agent
  spendlease keys provider set openai --key sk-...
  spendlease serve

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

// storeFlag adds the shared datastore flag without exposing an environment
// supplied DSN in generated help output. flag normally prints the concrete
// default, which may contain a database password.
func storeFlag(fs *flag.FlagSet) *string {
	target := defaultStore()
	fs.StringVar(&target, "store", target, "SQLite path or PostgreSQL DSN")
	fs.Lookup("store").DefValue = "$" + EnvStore + " or ./spendlease.db"
	return &target
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
