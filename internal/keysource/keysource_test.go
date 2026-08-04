package keysource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/vault"
)

func TestResolveSources(t *testing.T) {
	t.Parallel()
	key, _ := vault.GenerateMasterKey()
	spec := Spec{ValueEnv: "KEY", FileEnv: "KEY_FILE", CommandEnv: "KEY_COMMAND", Label: "master key"}
	tests := []struct {
		name   string
		env    map[string]string
		file   []byte
		run    []byte
		source string
	}{
		{name: "environment", env: map[string]string{"KEY": key.Hex()}, source: "environment KEY"},
		{name: "file", env: map[string]string{"KEY_FILE": "/secret/key"}, file: []byte(key.Hex() + "\n"), source: "file /secret/key"},
		{name: "command", env: map[string]string{"KEY_COMMAND": `["secret-tool","read","master"]`}, run: []byte(key.Hex()), source: "command secret-tool"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			r := Resolver{
				LookupEnv: func(name string) (string, bool) { value, ok := test.env[name]; return value, ok },
				ReadFile:  func(string) ([]byte, error) { return append([]byte(nil), test.file...), nil },
				Run:       func(context.Context, string, ...string) ([]byte, error) { return append([]byte(nil), test.run...), nil },
			}
			got, err := r.Resolve(context.Background(), spec)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !got.Present || got.Key != key || got.Source != test.source {
				t.Fatalf("Resolve = %+v, want key from %q", got, test.source)
			}
		})
	}
}

func TestResolveRejectsConflictsAndBadCommand(t *testing.T) {
	t.Parallel()
	key, _ := vault.GenerateMasterKey()
	spec := Spec{ValueEnv: "KEY", FileEnv: "KEY_FILE", CommandEnv: "KEY_COMMAND", Label: "master key"}
	lookup := func(name string) (string, bool) {
		values := map[string]string{"KEY": key.Hex(), "KEY_FILE": "/secret/key"}
		value, ok := values[name]
		return value, ok
	}
	if _, err := (Resolver{LookupEnv: lookup}).Resolve(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting sources error = %v", err)
	}

	lookup = func(name string) (string, bool) {
		if name == "KEY_COMMAND" {
			return "secret-tool --unsafe-shell-string", true
		}
		return "", false
	}
	if _, err := (Resolver{LookupEnv: lookup}).Resolve(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "JSON array") {
		t.Fatalf("bad command error = %v", err)
	}
}

func TestResolveIsOptionalOrRequired(t *testing.T) {
	t.Parallel()
	empty := Resolver{LookupEnv: func(string) (string, bool) { return "", false }}
	spec := Spec{ValueEnv: "KEY", FileEnv: "KEY_FILE", CommandEnv: "KEY_COMMAND", Label: "master key"}
	if got, err := empty.Resolve(context.Background(), spec); err != nil || got.Present {
		t.Fatalf("optional Resolve = (%+v, %v)", got, err)
	}
	spec.Required = true
	if _, err := empty.Resolve(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("required error = %v", err)
	}
}

func TestResolveDoesNotExposeCommandOutputOnFailure(t *testing.T) {
	t.Parallel()
	const secret = "do-not-leak-this-secret"
	r := Resolver{
		LookupEnv: func(name string) (string, bool) {
			if name == "KEY_COMMAND" {
				return `["secret-tool"]`, true
			}
			return "", false
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(secret), errors.New("exit status 1")
		},
	}
	_, err := r.Resolve(context.Background(), Spec{ValueEnv: "KEY", FileEnv: "KEY_FILE", CommandEnv: "KEY_COMMAND", Label: "master key"})
	if err == nil {
		t.Fatal("command failure was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("command failure exposed secret output")
	}
}

func TestLimitedBufferDrainsWithoutRetainingExcess(t *testing.T) {
	t.Parallel()
	b := limitedBuffer{limit: 4}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 {
		t.Fatalf("Write = (%d, %v), want (6, nil)", n, err)
	}
	if got := b.String(); got != "abcd" || !b.overflow {
		t.Fatalf("buffer = %q, overflow = %v", got, b.overflow)
	}
}

func TestRunCommandIsBounded(t *testing.T) {
	t.Setenv("SPENDLEASE_KEYSOURCE_HELPER", "print")
	t.Setenv("SPENDLEASE_KEYSOURCE_OUTPUT", "resolved-key")
	got, err := runCommand(context.Background(), os.Args[0], "-test.run=TestKeySourceHelperProcess")
	if err != nil || string(got) != "resolved-key" {
		t.Fatalf("runCommand = (%q, %v)", got, err)
	}

	t.Setenv("SPENDLEASE_KEYSOURCE_HELPER", "overflow")
	output, err := runCommand(context.Background(), os.Args[0], "-test.run=TestKeySourceHelperProcess")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("overflow = (%d bytes, %v)", len(output), err)
	}

	t.Setenv("SPENDLEASE_KEYSOURCE_HELPER", "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := runCommand(ctx, os.Args[0], "-test.run=TestKeySourceHelperProcess"); err == nil {
		t.Fatal("timed-out command succeeded")
	}
}

func TestKeySourceHelperProcess(t *testing.T) {
	switch os.Getenv("SPENDLEASE_KEYSOURCE_HELPER") {
	case "":
		return
	case "print":
		_, _ = fmt.Fprint(os.Stdout, os.Getenv("SPENDLEASE_KEYSOURCE_OUTPUT"))
	case "overflow":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", maxSourceBytes+1))
	case "sleep":
		time.Sleep(5 * time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}
