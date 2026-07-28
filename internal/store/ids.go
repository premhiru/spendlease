package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// Identifier prefixes. An ID pasted into a bug report should say what it is
// without needing a lookup.
const (
	// PrincipalPrefix prefixes principal identifiers.
	PrincipalPrefix = "prn_"
	// RunPrefix prefixes run identifiers.
	RunPrefix = "run_"
	// LeasePrefix prefixes lease identifiers.
	LeasePrefix = "lse_"
	// ReservationPrefix prefixes reservation identifiers.
	ReservationPrefix = "rsv_"

	// PrincipalKeyPrefix prefixes the long-lived principal API key. This is a
	// secret; only its hash is ever stored.
	PrincipalKeyPrefix = "slk_"
	// LeaseTokenPrefix prefixes the short-lived lease token handed to an
	// agent. Also a secret, also stored only as a hash.
	LeaseTokenPrefix = "sll_"
)

// idBytes is the entropy in a generated identifier. 16 bytes is 128 bits,
// which makes collisions unreachable in practice.
const idBytes = 16

// secretBytes is the entropy in a generated key or token. 32 bytes is 256
// bits, matching the SHA-256 digest they are stored as.
const secretBytes = 32

// idEncoding is lowercase base32 without padding: case-insensitive when
// retyped, and free of the characters that need escaping in URLs and shells.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID returns a random prefixed identifier, for example
// "prn_k3n7qm2xd4vb6ry8ftgh5w".
//
// It panics if the system random source fails. That is not a condition any
// caller can sensibly handle, and continuing with predictable identifiers
// would be worse than stopping.
func NewID(prefix string) string {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("store: cannot read random bytes for an identifier: %v", err))
	}
	return prefix + idEncoding.EncodeToString(b)
}

// NewPrincipalID returns a fresh principal identifier.
func NewPrincipalID() string { return NewID(PrincipalPrefix) }

// NewRunID returns a fresh run identifier.
func NewRunID() string { return NewID(RunPrefix) }

// NewLeaseID returns a fresh lease identifier.
func NewLeaseID() string { return NewID(LeasePrefix) }

// NewReservationID returns a fresh reservation identifier.
func NewReservationID() string { return NewID(ReservationPrefix) }

// NewSecret returns a random secret with the given prefix, together with the
// SHA-256 hex digest that should be stored in its place.
//
// The plaintext is the only copy that will ever exist. Show it to the user
// once at creation and then discard it; it cannot be recovered from the hash,
// from the database, or from a backup.
func NewSecret(prefix string) (plaintext, hash string) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("store: cannot read random bytes for a secret: %v", err))
	}
	plaintext = prefix + idEncoding.EncodeToString(b)
	return plaintext, HashSecret(plaintext)
}

// NewPrincipalKey returns a fresh slk_ key and its hash.
func NewPrincipalKey() (plaintext, hash string) { return NewSecret(PrincipalKeyPrefix) }

// NewLeaseToken returns a fresh sll_ token and its hash.
func NewLeaseToken() (plaintext, hash string) { return NewSecret(LeaseTokenPrefix) }

// HashSecret returns the lowercase SHA-256 hex digest of a key or token.
//
// A plain hash is correct here, deliberately, where a password would need a
// slow KDF. These secrets are 256 bits of machine-generated entropy rather
// than something a human chose, so there is no dictionary to attack and
// nothing for bcrypt to slow down. Hashing is on the per-request
// authorization path, and making it slow would tax every proxied call.
func HashSecret(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// SecretMatches reports whether a presented secret hashes to the stored
// digest, comparing in constant time so that a caller cannot learn a valid
// prefix by measuring how long a rejection took.
func SecretMatches(plaintext, storedHash string) bool {
	got := HashSecret(plaintext)
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

// LooksLikePrincipalKey reports whether s has the shape of an slk_ key. It is
// a cheap guard for rejecting obviously wrong input early, never a
// substitute for verifying the hash.
func LooksLikePrincipalKey(s string) bool { return strings.HasPrefix(s, PrincipalKeyPrefix) }

// LooksLikeLeaseToken reports whether s has the shape of an sll_ token.
func LooksLikeLeaseToken(s string) bool { return strings.HasPrefix(s, LeaseTokenPrefix) }
