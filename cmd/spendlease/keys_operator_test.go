package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/premhiru/spendlease/internal/operator"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
)

var operatorTokenPattern = regexp.MustCompile(`slo_[a-z2-7]+`)

func TestOperatorCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spendlease.db")
	runCommand := func(args ...string) (string, string, int) {
		var stdout, stderr bytes.Buffer
		code := run(append([]string{"keys", "operator"}, args...), &stdout, &stderr)
		return stdout.String(), stderr.String(), code
	}

	out, stderr, code := runCommand("create", "--store", path, "--name", "alice", "--role", "admin")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, stderr)
	}
	oldToken := operatorTokenPattern.FindString(out)
	if oldToken == "" || strings.Count(out, oldToken) != 1 {
		t.Fatalf("create did not show exactly one operator token: %q", out)
	}
	if out, _, code = runCommand("list", "--store", path); code != 0 || !strings.Contains(out, "alice") || !strings.Contains(out, "admin") {
		t.Fatalf("list = %d: %q", code, out)
	}

	out, stderr, code = runCommand("rotate", "--store", path, "--name", "alice")
	if code != 0 {
		t.Fatalf("rotate = %d: %s", code, stderr)
	}
	newToken := operatorTokenPattern.FindString(out)
	if newToken == "" || newToken == oldToken {
		t.Fatalf("rotated token = %q", newToken)
	}

	st, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	if _, err := st.GetOperatorByTokenHash(context.Background(), operator.HashToken(oldToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old token lookup = %v, want ErrNotFound", err)
	}
	if _, err := st.GetOperatorByTokenHash(context.Background(), operator.HashToken(newToken)); err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	_ = st.Close()

	if _, stderr, code = runCommand("revoke", "--store", path, "--name", "alice"); code == 0 || !strings.Contains(stderr, "final active admin") {
		t.Fatalf("final-admin revoke = %d: %q", code, stderr)
	}
	if _, stderr, code = runCommand("create", "--store", path, "--name", "bob", "--role", "admin"); code != 0 {
		t.Fatalf("create bob = %d: %s", code, stderr)
	}
	if _, stderr, code = runCommand("revoke", "--store", path, "--name", "alice"); code != 0 {
		t.Fatalf("revoke alice = %d: %s", code, stderr)
	}
	if out, stderr, code = runCommand("audit", "--store", path, "--limit", "10"); code != 0 || !strings.Contains(out, "local-cli") {
		t.Fatalf("audit = %d: %s / %q", code, stderr, out)
	}
}
