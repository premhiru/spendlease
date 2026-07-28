package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string // substring expected on stdout
		wantErr  string // substring expected on stderr
	}{
		{
			name:     "no arguments prints usage and fails",
			args:     nil,
			wantCode: 2,
			wantErr:  "Usage:",
		},
		{
			name:     "unknown command is rejected",
			args:     []string{"frobnicate"},
			wantCode: 2,
			wantErr:  `unknown command "frobnicate"`,
		},
		{
			name:     "help succeeds on stdout",
			args:     []string{"help"},
			wantCode: 0,
			wantOut:  "spendlease <command>",
		},
		{
			name:     "help flag succeeds",
			args:     []string{"--help"},
			wantCode: 0,
			wantOut:  "spendlease <command>",
		},
		{
			name:     "version prints build information",
			args:     []string{"version"},
			wantCode: 0,
			wantOut:  "spendlease dev",
		},
		{
			name:     "version rejects arguments",
			args:     []string{"version", "extra"},
			wantCode: 2,
			wantErr:  "takes no arguments",
		},
		{
			name:     "serve accepts flags",
			args:     []string{"serve", "-addr", ":8080"},
			wantCode: 0,
			wantOut:  "not implemented yet",
		},
		{
			name:     "serve rejects unknown flags",
			args:     []string{"serve", "-nope"},
			wantCode: 1,
			wantErr:  "flag provided but not defined",
		},
		{
			name:     "demo accepts a target",
			args:     []string{"demo", "-target", "http://localhost:4000"},
			wantCode: 0,
			wantOut:  "http://localhost:4000",
		},
		{
			name:     "keys requires a subcommand",
			args:     []string{"keys"},
			wantCode: 2,
			wantErr:  "expected one of principal, run, lease, revoke",
		},
		{
			name:     "keys rejects an unknown subcommand",
			args:     []string{"keys", "nonsense"},
			wantCode: 2,
			wantErr:  `unknown subcommand "nonsense"`,
		},
		{
			name:     "keys accepts a known subcommand",
			args:     []string{"keys", "principal"},
			wantCode: 0,
			wantOut:  "keys principal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			got := run(tt.args, &stdout, &stderr)

			if got != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d\nstdout: %s\nstderr: %s",
					tt.args, got, tt.wantCode, stdout.String(), stderr.String())
			}
			if tt.wantOut != "" && !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantOut)
			}
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

// TestRedactStore guards the promise in SECURITY.md that credentials never
// reach the logs. A regression here leaks a database password on every start.
func TestRedactStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "sqlite path is unchanged",
			dsn:  "./spendlease.db",
			want: "./spendlease.db",
		},
		{
			name: "absolute sqlite path is unchanged",
			dsn:  "/var/lib/spendlease/spendlease.db",
			want: "/var/lib/spendlease/spendlease.db",
		},
		{
			name: "postgres password is redacted",
			dsn:  "postgres://admin:hunter2@db.internal:5432/spendlease",
			want: "postgres://***@db.internal:5432/spendlease",
		},
		{
			name: "postgres without credentials is unchanged",
			dsn:  "postgres://db.internal:5432/spendlease",
			want: "postgres://db.internal:5432/spendlease",
		},
		{
			name: "password containing an at sign is fully redacted",
			dsn:  "postgres://admin:p@ss@db.internal:5432/spendlease",
			want: "postgres://***@db.internal:5432/spendlease",
		},
		{
			name: "empty dsn is unchanged",
			dsn:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := redactStore(tt.dsn); got != tt.want {
				t.Errorf("redactStore(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
			if strings.Contains(redactStore(tt.dsn), "hunter2") {
				t.Error("redactStore leaked the password")
			}
		})
	}
}
