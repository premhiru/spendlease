package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// memStore is an in-memory CredentialStore.
type memStore struct {
	mu   sync.Mutex
	rows map[string]Credential
}

func newMemStore() *memStore { return &memStore{rows: map[string]Credential{}} }

func (m *memStore) PutCredential(_ context.Context, c Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[c.Provider] = c
	return nil
}

func (m *memStore) GetCredential(_ context.Context, provider string) (Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[provider]
	if !ok {
		return Credential{}, ErrNoCredential
	}
	return c, nil
}

func (m *memStore) ListCredentialProviders(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.rows))
	for k := range m.rows {
		out = append(out, k)
	}
	return out, nil
}

func (m *memStore) DeleteCredential(_ context.Context, provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, provider)
	return nil
}

func newTestVault(t *testing.T) (*Vault, *memStore, MasterKey) {
	t.Helper()

	key, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	st := newMemStore()
	v, err := New(key, st)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v, st, key
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, _, _ := newTestVault(t)

	const secret = "sk-proj-a-real-looking-openai-key"
	if err := v.Put(ctx, "openai", secret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := v.Get(ctx, "openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != secret {
		t.Errorf("Get returned %q, want the original key", got)
	}
}

// TestCiphertextDoesNotContainPlaintext is the basic promise: what lands in
// the database must not be the key.
func TestCiphertextDoesNotContainPlaintext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, st, _ := newTestVault(t)

	const secret = "sk-proj-supersecretvalue"
	if err := v.Put(ctx, "openai", secret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stored, err := st.GetCredential(ctx, "openai")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if strings.Contains(string(stored.Ciphertext), secret) {
		t.Fatal("the stored ciphertext contains the plaintext key")
	}
	if len(stored.Nonce) == 0 {
		t.Error("no nonce was stored")
	}
	// GCM appends a 16-byte authentication tag.
	if len(stored.Ciphertext) != len(secret)+16 {
		t.Errorf("ciphertext length = %d, want %d", len(stored.Ciphertext), len(secret)+16)
	}
}

// TestNonceIsFreshEveryWrite guards against nonce reuse, which in GCM is
// catastrophic rather than merely untidy.
func TestNonceIsFreshEveryWrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, st, _ := newTestVault(t)

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		if err := v.Put(ctx, "openai", "same-key-every-time"); err != nil {
			t.Fatalf("Put: %v", err)
		}
		c, _ := st.GetCredential(ctx, "openai")
		n := string(c.Nonce)
		if seen[n] {
			t.Fatal("a nonce was reused across writes")
		}
		seen[n] = true
	}
}

// TestCiphertextIsBoundToItsProvider covers why the provider name is used as
// additional authenticated data: a row must not be movable between providers.
func TestCiphertextIsBoundToItsProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, st, _ := newTestVault(t)

	if err := v.Put(ctx, "openai", "sk-openai-key"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Copy OpenAI's encrypted row into Anthropic's slot, as someone with
	// database access might.
	stolen, _ := st.GetCredential(ctx, "openai")
	stolen.Provider = "anthropic"
	if err := st.PutCredential(ctx, stolen); err != nil {
		t.Fatalf("PutCredential: %v", err)
	}

	got, err := v.Get(ctx, "anthropic")
	if err == nil {
		t.Fatalf("a relocated ciphertext decrypted to %q; it should have failed", got)
	}
	if !errors.Is(err, ErrDecrypt) {
		t.Errorf("error = %v, want ErrDecrypt", err)
	}
}

// TestWrongMasterKeyFailsClearly covers the realistic operational disaster:
// the master key changed and every stored credential is now unreadable.
func TestWrongMasterKeyFailsClearly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, st, _ := newTestVault(t)

	if err := v.Put(ctx, "openai", "sk-openai-key"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	otherKey, _ := GenerateMasterKey()
	other, err := New(otherKey, st)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = other.Get(ctx, "openai")
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("error = %v, want ErrDecrypt", err)
	}
	if !strings.Contains(err.Error(), "master key") {
		t.Errorf("error %q does not point at the master key as the cause", err)
	}
	if strings.Contains(err.Error(), "sk-openai-key") {
		t.Error("the error leaked the plaintext key")
	}
}

// TestTamperedCiphertextIsRejected: GCM authenticates, so a flipped bit must
// fail rather than decrypt to garbage.
func TestTamperedCiphertextIsRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, st, _ := newTestVault(t)

	if err := v.Put(ctx, "openai", "sk-openai-key"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	c, _ := st.GetCredential(ctx, "openai")
	c.Ciphertext[0] ^= 0xFF
	if err := st.PutCredential(ctx, c); err != nil {
		t.Fatalf("PutCredential: %v", err)
	}

	if _, err := v.Get(ctx, "openai"); !errors.Is(err, ErrDecrypt) {
		t.Errorf("error = %v, want ErrDecrypt", err)
	}
}

func TestRotation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, _, _ := newTestVault(t)

	if err := v.Put(ctx, "openai", "old-key"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := v.Put(ctx, "openai", "new-key"); err != nil {
		t.Fatalf("Put (rotate): %v", err)
	}

	got, err := v.Get(ctx, "openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "new-key" {
		t.Errorf("after rotation Get returned %q, want new-key", got)
	}
}

func TestMissingCredential(t *testing.T) {
	t.Parallel()

	v, _, _ := newTestVault(t)
	if _, err := v.Get(context.Background(), "cohere"); !errors.Is(err, ErrNoCredential) {
		t.Errorf("error = %v, want ErrNoCredential", err)
	}
}

func TestPutRejectsEmptyInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, _, _ := newTestVault(t)

	if err := v.Put(ctx, "", "key"); err == nil {
		t.Error("an empty provider was accepted")
	}
	if err := v.Put(ctx, "openai", ""); err == nil {
		t.Error("an empty api key was accepted")
	}
}

func TestMasterKeyParsing(t *testing.T) {
	t.Parallel()

	key, _ := GenerateMasterKey()
	hexKey := key.Hex()

	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "a generated key round-trips", in: hexKey},
		{name: "too short", in: "abcd", wantErr: true},
		{name: "not hex", in: strings.Repeat("z", 64), wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "31 bytes", in: strings.Repeat("ab", 31), wantErr: true},
		{name: "33 bytes", in: strings.Repeat("ab", 33), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseMasterKey(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if !errors.Is(err, ErrBadMasterKey) {
					t.Errorf("error = %v, want ErrBadMasterKey", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != key {
				t.Error("the parsed key does not match the original")
			}
		})
	}
}

// TestMasterKeyDoesNotStringify guards against the key leaking through a
// stray %v, a struct dump, or a log line.
func TestMasterKeyDoesNotStringify(t *testing.T) {
	t.Parallel()

	key, _ := GenerateMasterKey()
	rendered := key.String()

	if strings.Contains(rendered, key.Hex()) {
		t.Fatal("String() rendered the key material")
	}
	if !strings.Contains(rendered, "redacted") {
		t.Errorf("String() = %q, want it to say it is redacted", rendered)
	}

	// The formatted forms Go would reach for must also be safe.
	for _, format := range []string{"%v", "%s"} {
		if got := fmt.Sprintf(format, key); strings.Contains(got, key.Hex()) {
			t.Errorf("%s leaked the key material", format)
		}
	}
}

func TestGeneratedKeysAreUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k, err := GenerateMasterKey()
		if err != nil {
			t.Fatalf("GenerateMasterKey: %v", err)
		}
		if seen[k.Hex()] {
			t.Fatal("GenerateMasterKey produced a duplicate")
		}
		seen[k.Hex()] = true
	}
}

func TestProvidersAndDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, _, _ := newTestVault(t)

	for _, p := range []string{"openai", "anthropic"} {
		if err := v.Put(ctx, p, "key-for-"+p); err != nil {
			t.Fatalf("Put %s: %v", p, err)
		}
	}

	names, err := v.Providers(ctx)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("got %d providers, want 2", len(names))
	}

	if err := v.Delete(ctx, "openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := v.Get(ctx, "openai"); !errors.Is(err, ErrNoCredential) {
		t.Errorf("after delete, error = %v, want ErrNoCredential", err)
	}
}
