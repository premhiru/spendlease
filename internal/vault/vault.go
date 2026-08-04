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
	// RotateCredentials transforms every credential in one datastore
	// transaction. If transform fails, no ciphertext is changed.
	RotateCredentials(ctx context.Context, transform func(Credential) (Credential, error)) (int, error)
}

// Vault encrypts and decrypts vendor credentials.
//
// It is safe for concurrent use: the AEAD is stateless and the store is
// required to be concurrency-safe.
type Vault struct {
	primary cipher.AEAD
	readers []cipher.AEAD
	store   CredentialStore
}

// New returns a vault that encrypts under key and persists through store.
func New(key MasterKey, store CredentialStore) (*Vault, error) {
	return NewKeyring(key, nil, store)
}

// NewKeyring returns a vault that writes with primary and can also decrypt
// credentials written with previous keys. Previous keys exist only to make a
// staged online rotation possible and are never used for new writes.
func NewKeyring(primary MasterKey, previous []MasterKey, store CredentialStore) (*Vault, error) {
	primaryAEAD, err := newAEAD(primary)
	if err != nil {
		return nil, err
	}
	readers := []cipher.AEAD{primaryAEAD}
	for _, key := range previous {
		aead, err := newAEAD(key)
		if err != nil {
			return nil, err
		}
		readers = append(readers, aead)
	}
	return &Vault{primary: primaryAEAD, readers: readers, store: store}, nil
}

func newAEAD(key MasterKey) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("vault: creating cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: creating GCM: %w", err)
	}
	return aead, nil
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

	now := time.Now().UTC()
	plaintext := []byte(apiKey)
	credential, err := v.encrypt(provider, plaintext, now, now)
	clear(plaintext)
	if err != nil {
		return err
	}
	return v.store.PutCredential(ctx, credential)
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

	plaintext, err := v.decrypt(c)
	if err != nil {
		// Deliberately vague about the cause and silent about the contents:
		// the useful signal for an operator is "the master key does not match
		// this database", and anything more would describe key material.
		return "", fmt.Errorf("%w for %q: the master key may have changed", ErrDecrypt, provider)
	}
	value := string(plaintext)
	clear(plaintext)
	return value, nil
}

func (v *Vault) decrypt(c Credential) ([]byte, error) {
	for _, aead := range v.readers {
		plaintext, err := aead.Open(nil, c.Nonce, c.Ciphertext, []byte(c.Provider))
		if err == nil {
			return plaintext, nil
		}
	}
	return nil, ErrDecrypt
}

func (v *Vault) encrypt(provider string, plaintext []byte, createdAt, updatedAt time.Time) (Credential, error) {
	nonce := make([]byte, v.primary.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Credential{}, fmt.Errorf("vault: reading nonce: %w", err)
	}
	// The provider name is additional authenticated data. Moving ciphertext
	// between provider rows therefore makes decryption fail.
	ciphertext := v.primary.Seal(nil, nonce, plaintext, []byte(provider))
	return Credential{
		Provider: provider, Nonce: nonce, Ciphertext: ciphertext,
		CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC(),
	}, nil
}

// Rotate re-encrypts every stored credential under the primary key in one
// datastore transaction. Readers may use the configured previous keys while
// a staged deployment is in progress; writes always use the primary key.
func (v *Vault) Rotate(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	return v.store.RotateCredentials(ctx, func(current Credential) (Credential, error) {
		plaintext, err := v.decrypt(current)
		if err != nil {
			return Credential{}, fmt.Errorf("%w for %q: none of the configured master keys match", ErrDecrypt, current.Provider)
		}
		replacement, err := v.encrypt(current.Provider, plaintext, current.CreatedAt, now)
		clear(plaintext)
		return replacement, err
	})
}

// Verify decrypts every stored credential without returning any plaintext.
// Operators use it before and after a staged key rotation.
func (v *Vault) Verify(ctx context.Context) (int, error) {
	providers, err := v.store.ListCredentialProviders(ctx)
	if err != nil {
		return 0, err
	}
	for _, provider := range providers {
		credential, err := v.store.GetCredential(ctx, provider)
		if err != nil {
			return 0, err
		}
		plaintext, err := v.decrypt(credential)
		if err != nil {
			return 0, fmt.Errorf("%w for %q: the master key may have changed", ErrDecrypt, provider)
		}
		clear(plaintext)
	}
	return len(providers), nil
}

// Providers returns the providers that currently have a stored key.
func (v *Vault) Providers(ctx context.Context) ([]string, error) {
	return v.store.ListCredentialProviders(ctx)
}

// Delete removes a provider's stored key.
func (v *Vault) Delete(ctx context.Context, provider string) error {
	return v.store.DeleteCredential(ctx, provider)
}
