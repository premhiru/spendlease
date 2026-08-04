// Package keysource resolves vault master keys from external secret delivery
// mechanisms without coupling spendlease to any cloud vendor SDK.
package keysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/vault"
)

const (
	maxSourceBytes = 4096
	commandTimeout = 15 * time.Second
)

// Spec names the mutually exclusive environment variables for one key.
type Spec struct {
	ValueEnv   string
	FileEnv    string
	CommandEnv string
	Required   bool
	Label      string
}

// Result is one resolved key and a safe description of its source.
type Result struct {
	Key     vault.MasterKey
	Source  string
	Present bool
}

// Resolver makes source I/O replaceable in tests. Zero fields use the secure
// operating-system implementations.
type Resolver struct {
	LookupEnv func(string) (string, bool)
	ReadFile  func(string) ([]byte, error)
	Run       func(context.Context, string, ...string) ([]byte, error)
}

// Resolve loads and parses one key. Value, file and command sources are
// mutually exclusive so a stale environment variable cannot silently shadow
// a newly configured secret manager.
func (r Resolver) Resolve(ctx context.Context, spec Spec) (Result, error) {
	lookup := r.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	type configuredSource struct {
		kind  string
		name  string
		value string
	}
	var configured []configuredSource
	for _, source := range []configuredSource{
		{kind: "environment", name: spec.ValueEnv},
		{kind: "file", name: spec.FileEnv},
		{kind: "command", name: spec.CommandEnv},
	} {
		if value, ok := lookup(source.name); ok && strings.TrimSpace(value) != "" {
			source.value = strings.TrimSpace(value)
			configured = append(configured, source)
		}
	}
	if len(configured) == 0 {
		if spec.Required {
			return Result{}, fmt.Errorf("%s is required; set exactly one of %s, %s, or %s",
				spec.Label, spec.ValueEnv, spec.FileEnv, spec.CommandEnv)
		}
		return Result{}, nil
	}
	if len(configured) > 1 {
		names := make([]string, 0, len(configured))
		for _, source := range configured {
			names = append(names, source.name)
		}
		return Result{}, fmt.Errorf("%s has conflicting sources: %s; set exactly one",
			spec.Label, strings.Join(names, ", "))
	}

	source := configured[0]
	var raw []byte
	var description string
	switch source.kind {
	case "environment":
		raw = []byte(source.value)
		description = "environment " + source.name
	case "file":
		readFile := r.ReadFile
		if readFile == nil {
			readFile = readLimitedFile
		}
		var err error
		raw, err = readFile(source.value)
		if err != nil {
			return Result{}, fmt.Errorf("reading %s from %s: %w", spec.Label, source.name, err)
		}
		description = "file " + source.value
	case "command":
		argv, err := parseCommand(source.value)
		if err != nil {
			return Result{}, fmt.Errorf("parsing %s: %w", source.name, err)
		}
		run := r.Run
		if run == nil {
			run = runCommand
		}
		commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		raw, err = run(commandCtx, argv[0], argv[1:]...)
		if err != nil {
			return Result{}, fmt.Errorf("resolving %s with %s: %w", spec.Label, source.name, err)
		}
		description = "command " + filepath.Base(argv[0])
	default:
		panic("unreachable key source")
	}
	if len(raw) > maxSourceBytes {
		clear(raw)
		return Result{}, fmt.Errorf("%s output exceeds %d bytes", spec.Label, maxSourceBytes)
	}
	value := strings.TrimSpace(string(raw))
	clear(raw)
	key, err := vault.ParseMasterKey(value)
	if err != nil {
		return Result{}, fmt.Errorf("%s from %s is unusable: %w", spec.Label, source.name, err)
	}
	return Result{Key: key, Source: description, Present: true}, nil
}

func parseCommand(raw string) ([]string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var argv []string
	if err := decoder.Decode(&argv); err != nil {
		return nil, errors.New("must be a JSON array of executable and arguments")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("must contain one JSON array only")
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, errors.New("must include an executable as the first array item")
	}
	return argv, nil
}

func readLimitedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
}

type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		toWrite := len(p)
		if toWrite > remaining {
			toWrite = remaining
		}
		_, _ = b.buf.Write(p[:toWrite])
	}
	if len(p) > remaining {
		b.overflow = true
	}
	// Report the whole input consumed so os/exec keeps draining the pipe. The
	// excess bytes are discarded rather than retained in memory.
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *limitedBuffer) String() string { return b.buf.String() }

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout limitedBuffer
	stdout.limit = maxSourceBytes
	cmd.Stdout = &stdout
	// Secret-manager diagnostics can echo request data. Do not surface or
	// retain stderr; the exit status and configured variable identify the
	// failing integration without risking key disclosure.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		clear(stdout.Bytes())
		return nil, fmt.Errorf("key source command failed: %w", err)
	}
	if stdout.overflow {
		clear(stdout.Bytes())
		return nil, fmt.Errorf("key source command output exceeds %d bytes", maxSourceBytes)
	}
	return stdout.Bytes(), nil
}
