// Package vault stores vendor API credentials encrypted at rest.
//
// Agents never hold a vendor key. The real keys live here, encrypted with
// AES-256-GCM under a master key, and the gateway fetches one only at the
// moment it needs to authenticate an outbound request.
//
// SECURITY.md makes three promises about credential handling, and this
// package is where two of them are kept: vendor keys are encrypted at rest,
// and key material is never logged.
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// KeySize is the length of a master key in bytes. AES-256 requires 32.
const KeySize = 32

// Errors returned by this package.
var (
	// ErrNoCredential means no key has been stored for that provider.
	ErrNoCredential = errors.New("vault: no credential for provider")

	// ErrBadMasterKey means the configured master key is malformed or the
	// wrong length.
	ErrBadMasterKey = errors.New("vault: invalid master key")

	// ErrDecrypt means a stored credential could not be decrypted. In
	// practice this means the master key changed, or the row was tampered
	// with. It is never a transient failure and retrying will not help.
	ErrDecrypt = errors.New("vault: cannot decrypt credential")
)

// MasterKey is the 32-byte key that every stored credential is encrypted
// under. Losing it means losing every stored vendor key; they must be
// re-entered, not recovered.
type MasterKey [KeySize]byte

// GenerateMasterKey returns a new random master key.
func GenerateMasterKey() (MasterKey, error) {
	var k MasterKey
	if _, err := rand.Read(k[:]); err != nil {
		return MasterKey{}, fmt.Errorf("vault: reading random bytes: %w", err)
	}
	return k, nil
}

// ParseMasterKey decodes a 64-character hex string into a master key.
func ParseMasterKey(s string) (MasterKey, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return MasterKey{}, fmt.Errorf("%w: not valid hex", ErrBadMasterKey)
	}
	if len(b) != KeySize {
		return MasterKey{}, fmt.Errorf("%w: got %d bytes, want %d", ErrBadMasterKey, len(b), KeySize)
	}
	var k MasterKey
	copy(k[:], b)
	return k, nil
}

// Hex renders the key for writing to a key file.
//
// The result is the key itself. Never log it, never include it in an error,
// and never put it in a response body.
func (k MasterKey) Hex() string { return hex.EncodeToString(k[:]) }

// String deliberately does not render the key, so that a stray %v or a struct
// dump cannot leak it into a log line.
func (k MasterKey) String() string { return "vault.MasterKey(redacted)" }

// Credential is one provider's encrypted vendor key as it is stored.
type Credential struct {
	// Provider is the vendor name, for example "openai".
	Provider string
	// Nonce is the AES-GCM nonce, fresh for every write.
	Nonce []byte
	// Ciphertext is the encrypted vendor key with its authentication tag.
	Ciphertext []byte
	// CreatedAt is when the credential was first stored.
	CreatedAt time.Time
	// UpdatedAt is when it was last rotated.
	UpdatedAt time.Time
}

// CredentialStore is the persistence this package needs. It is a narrow
// interface rather than the whole Store so the vault can be tested against a
// map, and so it is obvious that the vault touches nothing else.
type CredentialStore interface {
	// PutCredential inserts or replaces a provider's credential.
	PutCredential(ctx context.Context, c Credential) error
	// GetCredential returns a provider's credential.
	GetCredential(ctx context.Context, provider string) (Credential, error)
	// ListCredentialProviders returns the providers that have a stored key.
	ListCredentialProviders(ctx context.Context) ([]string, error)
	// DeleteCredential removes a provider's credential.
	DeleteCredential(ctx context.Context, provider string) error
}

// Vault encrypts and decrypts vendor credentials.
//
// It is safe for concurrent use: the AEAD is stateless and the store is
// required to be concurrency-safe.
type Vault struct {
	aead  cipher.AEAD
	store CredentialStore
}

// New returns a vault that encrypts under key and persists through store.
func New(key MasterKey, store CredentialStore) (*Vault, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("vault: creating cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: creating GCM: %w", err)
	}
	return &Vault{aead: aead, store: store}, nil
}

// Put encrypts and stores a vendor API key, replacing any existing key for
// that provider. Rotating a key is exactly this call.
func (v *Vault) Put(ctx context.Context, provider, apiKey string) error {
	if provider == "" {
		return errors.New("vault: provider must not be empty")
	}
	if apiKey == "" {
		return errors.New("vault: api key must not be empty")
	}

	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("vault: reading nonce: %w", err)
	}

	// The provider name is additional authenticated data. It is not secret,
	// but binding it to the ciphertext means a row cannot be copied from one
	// provider to another: decryption of a moved ciphertext fails rather than
	// silently returning the wrong vendor's key.
	ciphertext := v.aead.Seal(nil, nonce, []byte(apiKey), []byte(provider))

	now := time.Now().UTC()
	return v.store.PutCredential(ctx, Credential{
		Provider:   provider,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// Get decrypts and returns a provider's vendor API key.
//
// The returned string is live key material. Do not log it, do not store it,
// and do not put it in an error message.
func (v *Vault) Get(ctx context.Context, provider string) (string, error) {
	c, err := v.store.GetCredential(ctx, provider)
	if err != nil {
		return "", err
	}

	plaintext, err := v.aead.Open(nil, c.Nonce, c.Ciphertext, []byte(provider))
	if err != nil {
		// Deliberately vague about the cause and silent about the contents:
		// the useful signal for an operator is "the master key does not match
		// this database", and anything more would describe key material.
		return "", fmt.Errorf("%w for %q: the master key may have changed", ErrDecrypt, provider)
	}
	return string(plaintext), nil
}

// Providers returns the providers that currently have a stored key.
func (v *Vault) Providers(ctx context.Context) ([]string, error) {
	return v.store.ListCredentialProviders(ctx)
}

// Delete removes a provider's stored key.
func (v *Vault) Delete(ctx context.Context, provider string) error {
	return v.store.DeleteCredential(ctx, provider)
}
