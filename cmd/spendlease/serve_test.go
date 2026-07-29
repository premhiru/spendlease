package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundToLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:4000", true},
		{"[::1]:4000", true},
		{"localhost:4000", true},
		{"0.0.0.0:4000", false},
		{"192.168.1.20:4000", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()
			if got := boundToLoopback(tt.addr); got != tt.want {
				t.Errorf("boundToLoopback(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestReportAdminAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, addr, token, want string
	}{
		{"loopback is quiet", "127.0.0.1:4000", "", ""},
		{"remote without token explains refusal", "0.0.0.0:4000", "", EnvAdminToken},
		{"remote with token confirms protection", "0.0.0.0:4000", "secret", "token is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			reportAdminAccess(&out, tt.addr, tt.token)
			if tt.want == "" && out.Len() != 0 {
				t.Errorf("output = %q, want silence", out.String())
			}
			if tt.want != "" && !strings.Contains(out.String(), tt.want) {
				t.Errorf("output = %q, want it to contain %q", out.String(), tt.want)
			}
		})
	}
}

// TestServeRefusesProductionBeforeTouchingDisk goes through run() rather than
// resolveMasterKey, because the bug it guards against was one of ordering
// rather than logic.
//
// The unit test for resolveMasterKey already passed while the real binary
// created and migrated a database and only then refused to start. A startup
// that was always going to be rejected must not leave artefacts behind.
func TestServeRefusesProductionBeforeTouchingDisk(t *testing.T) {
	t.Setenv(EnvEnv, "production")
	t.Setenv(EnvMasterKey, "")

	dir := t.TempDir()
	storePath := filepath.Join(dir, "spendlease.db")

	var stdout, stderr bytes.Buffer
	code := run([]string{"serve", "--store", storePath, "--addr", "127.0.0.1:0"}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("serve succeeded in production without a master key; stdout: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), EnvMasterKey) {
		t.Errorf("stderr %q does not name the variable to set", stderr.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the store directory: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a refused startup created %v; it must fail before any side effects", names)
	}
}

// TestServeValidatesFlagsBeforeSideEffects checks the same principle for the
// cheaper failures: a bad flag must not leave a database behind either.
func TestServeValidatesFlagsBeforeSideEffects(t *testing.T) {
	t.Setenv(EnvEnv, "")
	t.Setenv(EnvMasterKey, "")

	tests := []struct {
		name string
		args []string
	}{
		{name: "bad log level", args: []string{"serve", "--log-level", "chatty"}},
		{name: "bad openai url", args: []string{"serve", "--openai-url", "://nope"}},
		{name: "bad anthropic url", args: []string{"serve", "--anthropic-url", "://nope"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append(append([]string{}, tt.args...), "--store", filepath.Join(dir, "spendlease.db"))

			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code == 0 {
				t.Fatalf("serve accepted %v", tt.args)
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("reading the store directory: %v", err)
			}
			if len(entries) != 0 {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("a startup rejected for bad configuration created %v", names)
			}
		})
	}
}
