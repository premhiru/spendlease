package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAndLeaseCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spendlease.db")
	runCommand := func(args ...string) string {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 0 {
			t.Fatalf("run %v = %d: %s", args, code, errOut.String())
		}
		return out.String()
	}
	runCommand("keys", "principal", "create", "--store", path, "--name", "agent")
	out := runCommand("keys", "run", "create", "--store", path, "--principal", "agent", "--budget", "2.00")
	var runID string
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "run_") {
			runID = field
			break
		}
	}
	if runID == "" {
		t.Fatalf("run output has no run ID: %s", out)
	}
	out = runCommand("keys", "lease", "issue", "--store", path, "--run", runID, "--ttl", "1m", "--providers", "openai")
	if !strings.Contains(out, "sll_") {
		t.Fatalf("lease output has no shown-once token: %s", out)
	}
	out = runCommand("keys", "revoke", "--store", path, "--all", "--principal", "agent")
	if !strings.Contains(out, "Revoked 1 lease") {
		t.Fatalf("revoke output = %s", out)
	}
}
