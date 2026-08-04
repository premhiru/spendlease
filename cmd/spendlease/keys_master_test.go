package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/premhiru/spendlease/internal/store/sqlite"
	"github.com/premhiru/spendlease/internal/vault"
)

func TestMasterKeyRotateCLI(t *testing.T) {
	oldKey, _ := vault.GenerateMasterKey()
	newKey, _ := vault.GenerateMasterKey()
	path := filepath.Join(t.TempDir(), "spendlease.db")
	t.Setenv(EnvMasterKey, oldKey.Hex())
	t.Setenv(EnvPreviousMasterKey, "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"keys", "provider", "set", "openai", "--store", path, "--key", "vendor-secret"}, &stdout, &stderr); code != 0 {
		t.Fatalf("provider set = %d: %s", code, stderr.String())
	}

	t.Setenv(EnvMasterKey, newKey.Hex())
	t.Setenv(EnvPreviousMasterKey, oldKey.Hex())
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"keys", "master", "rotate", "--store", path, "--confirm"}, &stdout, &stderr); code != 0 {
		t.Fatalf("master rotate = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Rotated 1 vendor credential") {
		t.Fatalf("rotate output = %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"keys", "master", "verify", "--store", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("master verify = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Verified 1 vendor credential") {
		t.Fatalf("verify output = %q", stdout.String())
	}

	st, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	newVault, _ := vault.New(newKey, st)
	if got, err := newVault.Get(context.Background(), "openai"); err != nil || got != "vendor-secret" {
		t.Fatalf("new key Get = (%q, %v)", got, err)
	}
	oldVault, _ := vault.New(oldKey, st)
	if _, err := oldVault.Get(context.Background(), "openai"); err == nil || !strings.Contains(err.Error(), "cannot decrypt") {
		t.Fatalf("old key unexpectedly decrypts: %v", err)
	}
}

func TestMasterKeyRotateRequiresConfirmationAndPreviousKey(t *testing.T) {
	key, _ := vault.GenerateMasterKey()
	t.Setenv(EnvMasterKey, key.Hex())
	t.Setenv(EnvPreviousMasterKey, "")
	path := filepath.Join(t.TempDir(), "spendlease.db")

	for _, args := range [][]string{
		{"keys", "master", "rotate", "--store", path},
		{"keys", "master", "rotate", "--store", path, "--confirm"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("run(%v) = %d, want usage error: %s", args, code, stderr.String())
		}
	}
}
