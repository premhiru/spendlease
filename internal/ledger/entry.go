// Package ledger defines the append-only spend record and the tamper-evident
// hash chain that links it together.
//
// This package is deliberately free of any database dependency. It defines
// what an entry is, how its hash is computed, and how a chain is verified;
// persisting entries is the store's job. Keeping the hashing pure is what
// makes it testable without a database and identical across every backend.
package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/premhiru/spendlease/internal/money"
)

// GenesisHash is the PrevHash of the first entry in a chain. It is 32 zero
// bytes in hex, a value no SHA-256 output will realistically collide with,
// which makes "is this the first entry?" answerable without a separate flag.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Entry is one immutable record of money spent.
//
// Once written it is never updated and never deleted; the store enforces that
// with a database trigger rather than trusting application code. Correcting a
// mistake means appending a compensating entry, exactly as a paper ledger
// would.
type Entry struct {
	// Seq is the position in the chain, starting at 1 and assigned by the
	// store on append. It is the ordering the hash chain depends on.
	Seq int64

	// RunID is the run this spend is charged to.
	RunID string

	// PrincipalID is denormalised from the run so that per-principal totals
	// never require a join against a run that may since have been closed.
	PrincipalID string

	// Provider and Model identify what was called, for example "openai" and
	// "gpt-4o".
	Provider string
	Model    string

	// InputTokens and OutputTokens are the usage actually reported by the
	// provider, or the best estimate available if the request did not
	// complete cleanly.
	InputTokens  int64
	OutputTokens int64

	// Cost is the amount charged, in nanodollars.
	Cost money.Nanos

	// Estimated marks an entry whose cost did not come from a known price and
	// reported usage — an unknown model, a fallback rate, or a settlement from
	// partial usage after a client disconnected. Never silently zero.
	Estimated bool

	// CreatedAt is when the entry was appended, always stored in UTC.
	CreatedAt time.Time

	// PrevHash is the Hash of the entry before this one, or GenesisHash.
	PrevHash string

	// Hash is this entry's own hash, covering every field above including
	// PrevHash. Computed by ComputeHash and set by the store on append.
	Hash string
}

// ComputeHash returns the SHA-256 hash of the entry's contents chained onto
// prevHash. It does not read or modify e.Hash, so it can be used both to seal
// a new entry and to re-derive an existing one during verification.
//
// The serialisation is length-prefixed field by field. Simple concatenation
// or a separator character would let a crafted value forge a different
// entry's digest — a model named "a|b" and a provider "a" with model "b"
// would otherwise hash identically. Length prefixes make that impossible.
func (e Entry) ComputeHash(prevHash string) string {
	h := sha256.New()

	write := func(s string) {
		// The length prefix is itself unambiguous because it is terminated
		// by a colon that cannot appear in a decimal number.
		_, _ = h.Write([]byte(strconv.Itoa(len(s))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(s))
	}

	write(strconv.FormatInt(e.Seq, 10))
	write(e.RunID)
	write(e.PrincipalID)
	write(e.Provider)
	write(e.Model)
	write(strconv.FormatInt(e.InputTokens, 10))
	write(strconv.FormatInt(e.OutputTokens, 10))
	write(strconv.FormatInt(int64(e.Cost), 10))
	write(strconv.FormatBool(e.Estimated))
	// RFC 3339 with nanoseconds, normalised to UTC, so the same instant hashes
	// identically regardless of the process's local zone.
	write(e.CreatedAt.UTC().Format(time.RFC3339Nano))
	write(prevHash)

	return hex.EncodeToString(h.Sum(nil))
}

// Seal returns a copy of the entry with PrevHash and Hash set, ready to be
// appended after the entry whose hash is prevHash.
func (e Entry) Seal(prevHash string) Entry {
	e.PrevHash = prevHash
	e.Hash = e.ComputeHash(prevHash)
	return e
}

// ChainError describes exactly where and how a chain failed verification.
// It names the sequence number so an operator can go and look at that row.
type ChainError struct {
	// Seq is the sequence number of the entry that failed.
	Seq int64
	// Reason explains what was wrong.
	Reason string
	// Want and Got are the expected and actual values, when relevant.
	Want, Got string
}

// Error implements the error interface.
func (e *ChainError) Error() string {
	if e.Want == "" && e.Got == "" {
		return fmt.Sprintf("ledger: chain broken at seq %d: %s", e.Seq, e.Reason)
	}
	return fmt.Sprintf("ledger: chain broken at seq %d: %s (want %s, got %s)",
		e.Seq, e.Reason, e.Want, e.Got)
}

// VerifyChain checks that entries form an unbroken hash chain in the order
// given. It reports the first problem found, if any.
//
// It verifies three things: that the first entry chains onto GenesisHash,
// that each subsequent entry's PrevHash is its predecessor's Hash, and that
// every entry's stored Hash still matches its recomputed contents. The third
// check is what turns the chain from an ordering guarantee into a tamper
// detector — editing any field of any historical row invalidates that row and
// every row after it.
//
// An empty slice verifies successfully; a ledger with nothing in it is not
// broken.
func VerifyChain(entries []Entry) error {
	prev := GenesisHash

	for _, e := range entries {
		if e.PrevHash != prev {
			return &ChainError{
				Seq:    e.Seq,
				Reason: "prev_hash does not match the preceding entry",
				Want:   prev,
				Got:    e.PrevHash,
			}
		}

		want := e.ComputeHash(e.PrevHash)
		if e.Hash != want {
			return &ChainError{
				Seq:    e.Seq,
				Reason: "entry contents do not match its stored hash",
				Want:   want,
				Got:    e.Hash,
			}
		}

		prev = e.Hash
	}

	return nil
}
