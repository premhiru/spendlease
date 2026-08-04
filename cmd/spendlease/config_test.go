package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/premhiru/spendlease/internal/vault"
)

func TestKeyFilePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		store string
		want  string
	}{
		{"relative db", "spendlease.db", "spendlease.key"},
		{"db in a directory", filepath.Join("data", "spendlease.db"), filepath.Join("data", "spendlease.key")},
		{"no extension", "mydata", "mydata.key"},
		{"other extension", "store.sqlite3", "store.key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := keyFilePath(tt.store); got != tt.want {
				t.Errorf("keyFilePath(%q) = %q, want %q", tt.store, got, tt.want)
			}
		})
	}
}

// TestResolveMasterKeyFromEnvironment covers the production path.
func TestResolveMasterKeyFromEnvironment(t *testing.T) {
	want, err := vault.GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	t.Setenv(EnvMasterKey, want.Hex())

	got, source, err := resolveMasterKey(filepath.Join(t.TempDir(), "spendlease.db"))
	if err != nil {
		t.Fatalf("resolveMasterKey: %v", err)
	}
	if got != want {
		t.Error("the resolved key does not match the environment")
	}
	if source != "environment "+EnvMasterKey {
		t.Errorf("source = %q, want environment source", source)
	}
}

func TestResolveMasterKeysWithPreviousFallback(t *testing.T) {
	primary, _ := vault.GenerateMasterKey()
	previous, _ := vault.GenerateMasterKey()
	t.Setenv(EnvMasterKey, primary.Hex())
	t.Setenv(EnvPreviousMasterKey, previous.Hex())

	got, source, err := resolveMasterKeys(context.Background(), filepath.Join(t.TempDir(), "spendlease.db"))
	if err != nil {
		t.Fatalf("resolveMasterKeys: %v", err)
	}
	if got.Primary != primary || len(got.Previous) != 1 || got.Previous[0] != previous {
		t.Fatalf("resolved keyring does not match configured keys")
	}
	if !strings.Contains(source, EnvPreviousMasterKey) {
		t.Fatalf("source = %q, want previous source", source)
	}
}

func TestResolveMasterKeyFromMountedFile(t *testing.T) {
	key, _ := vault.GenerateMasterKey()
	path := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(path, []byte(key.Hex()+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvMasterKeyFile, path)

	got, source, err := resolveMasterKeys(context.Background(), filepath.Join(t.TempDir(), "spendlease.db"))
	if err != nil {
		t.Fatalf("resolveMasterKeys: %v", err)
	}
	if got.Primary != key || !got.ExplicitPrimary || !strings.Contains(source, path) {
		t.Fatalf("mounted-file resolution = (%+v, %q)", got, source)
	}
}

func TestResolveAdminToken(t *testing.T) {
	t.Setenv(EnvAdminToken, "  from-environment  ")

	if got := resolveAdminToken(""); got != "from-environment" {
		t.Errorf("environment token = %q, want from-environment", got)
	}
	if got := resolveAdminToken("  from-flag  "); got != "from-flag" {
		t.Errorf("flag token = %q, want from-flag", got)
	}
	if got := resolveAdminToken("   "); got != "from-environment" {
		t.Errorf("blank flag did not fall back to environment: %q", got)
	}
}

func TestDefaultStore(t *testing.T) {
	t.Setenv(EnvStore, "  postgres://db.example/spendlease  ")
	if got := defaultStore(); got != "postgres://db.example/spendlease" {
		t.Fatalf("defaultStore() = %q", got)
	}
	t.Setenv(EnvStore, "")
	if got := defaultStore(); got != "./spendlease.db" {
		t.Fatalf("empty defaultStore() = %q", got)
	}
}

func TestResolveMasterKeyRejectsMalformedEnvironment(t *testing.T) {
	t.Setenv(EnvMasterKey, "not-a-valid-hex-key")

	_, _, err := resolveMasterKey(filepath.Join(t.TempDir(), "spendlease.db"))
	if err == nil {
		t.Fatal("a malformed master key was accepted")
	}
	// The message has to be actionable: say what is wrong and how to fix it.
	for _, want := range []string{EnvMasterKey, "hex characters", "keys master generate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestInvalidPreviousKeyDoesNotCreateDevelopmentKey(t *testing.T) {
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvPreviousMasterKey, "not-a-key")
	t.Setenv(EnvEnv, "")
	dir := t.TempDir()
	if _, _, err := resolveMasterKeys(context.Background(), filepath.Join(dir, "spendlease.db")); err == nil {
		t.Fatal("invalid previous key was accepted")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("invalid configuration created %d file(s)", len(entries))
	}
}

// TestResolveMasterKeyGeneratesAndPersists covers the zero-config developer
// path that the quickstart depends on.
func TestResolveMasterKeyGeneratesAndPersists(t *testing.T) {
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvEnv, "")

	dir := t.TempDir()
	storePath := filepath.Join(dir, "spendlease.db")

	first, source, err := resolveMasterKey(storePath)
	if err != nil {
		t.Fatalf("resolveMasterKey: %v", err)
	}
	if !strings.Contains(source, "newly generated") {
		t.Errorf("source = %q, want it to say the key was generated", source)
	}

	keyPath := keyFilePath(storePath)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("the key file was not written: %v", err)
	}

	// Owner-only permissions. Windows does not model Unix bits, so this is
	// only meaningful where it is meaningful.
	if os.PathSeparator == '/' {
		if perm := info.Mode().Perm(); perm != keyFilePerm {
			t.Errorf("key file permissions = %o, want %o", perm, keyFilePerm)
		}
	}

	// A second call must reuse the same key, not mint a new one; otherwise a
	// restart would orphan every stored credential.
	second, source, err := resolveMasterKey(storePath)
	if err != nil {
		t.Fatalf("second resolveMasterKey: %v", err)
	}
	if second != first {
		t.Error("restarting generated a different master key, which would orphan stored credentials")
	}
	if !strings.Contains(source, "key file") {
		t.Errorf("source = %q, want it to say the key came from the key file", source)
	}
}

// TestProductionRefusesImplicitKey is the guard that stops a deployment
// encrypting credentials under a key sitting next to them.
func TestProductionRefusesImplicitKey(t *testing.T) {
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvEnv, "production")

	dir := t.TempDir()
	_, _, err := resolveMasterKey(filepath.Join(dir, "spendlease.db"))
	if err == nil {
		t.Fatal("production accepted an implicitly generated master key")
	}
	if !strings.Contains(err.Error(), EnvMasterKey) {
		t.Errorf("error %q does not name the variable to set", err)
	}

	// And nothing was written to disk.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("production wrote %d file(s) despite refusing to start", len(entries))
	}
}

func TestPostgresRefusesImplicitKeyOutsideProduction(t *testing.T) {
	t.Setenv(EnvMasterKey, "")
	t.Setenv(EnvEnv, "development")

	_, _, err := resolveMasterKey("postgres://user:secret@db.example/spendlease")
	if err == nil {
		t.Fatal("PostgreSQL accepted an implicitly generated master key")
	}
	for _, want := range []string{EnvMasterKey, "PostgreSQL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestProductionAcceptsExplicitKey confirms the refusal is about implicitness,
// not about production itself.
func TestProductionAcceptsExplicitKey(t *testing.T) {
	key, _ := vault.GenerateMasterKey()
	t.Setenv(EnvEnv, "production")
	t.Setenv(EnvMasterKey, key.Hex())

	got, _, err := resolveMasterKey(filepath.Join(t.TempDir(), "spendlease.db"))
	if err != nil {
		t.Fatalf("resolveMasterKey: %v", err)
	}
	if got != key {
		t.Error("the resolved key does not match the environment")
	}
}

func TestTrimSpace(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"sk-abc", "sk-abc"},
		{"  sk-abc  ", "sk-abc"},
		{"sk-abc\n", "sk-abc"},
		{"sk-abc\r\n", "sk-abc"},
		{"\t sk-abc \t", "sk-abc"},
		{"", ""},
		{"   ", ""},
	}

	for _, tt := range tests {
		if got := trimSpace(tt.in); got != tt.want {
			t.Errorf("trimSpace(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
