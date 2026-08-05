package ledger

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/billing"
	"github.com/premhiru/spendlease/internal/money"
)

func testEntry(seq int64) Entry {
	return Entry{
		Seq:          seq,
		RunID:        "run_abc",
		PrincipalID:  "prn_xyz",
		Provider:     "openai",
		Model:        "gpt-4o",
		InputTokens:  1000,
		OutputTokens: 500,
		Cost:         money.MustParseUSD("0.0075"),
		CreatedAt:    time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	}
}

// buildChain seals n entries into a valid chain.
func buildChain(n int) []Entry {
	chain := make([]Entry, 0, n)
	prev := GenesisHash
	for i := 1; i <= n; i++ {
		e := testEntry(int64(i)).Seal(prev)
		chain = append(chain, e)
		prev = e.Hash
	}
	return chain
}

func TestComputeHashIsDeterministic(t *testing.T) {
	t.Parallel()

	e := testEntry(1)
	if a, b := e.ComputeHash(GenesisHash), e.ComputeHash(GenesisHash); a != b {
		t.Errorf("ComputeHash is not deterministic: %s != %s", a, b)
	}
}

func TestVersionTwoHashCoversUsageAndPricingProvenance(t *testing.T) {
	t.Parallel()

	base := testEntry(1)
	base.HashVersion = HashVersionUsage
	base.Usage = billing.TokenUsage(900, 100, 0, 0, 500)
	base.ExternalID = "req_123"
	base.PricingRevision = "abcdef012345"
	base.PriceEffective = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	baseHash := base.ComputeHash(GenesisHash)

	tests := []struct {
		name   string
		mutate func(*Entry)
	}{
		{"Usage", func(e *Entry) { e.Usage[billing.UnitCachedInputTokens]++ }},
		{"ExternalID", func(e *Entry) { e.ExternalID = "req_other" }},
		{"PricingRevision", func(e *Entry) { e.PricingRevision = "other" }},
		{"PriceEffective", func(e *Entry) { e.PriceEffective = e.PriceEffective.Add(time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := base
			copy.Usage = base.Usage.Normalized()
			tt.mutate(&copy)
			if got := copy.ComputeHash(GenesisHash); got == baseHash {
				t.Fatalf("changing %s did not change the v2 hash", tt.name)
			}
		})
	}
}

func TestLegacyHashStillVerifies(t *testing.T) {
	t.Parallel()
	e := testEntry(1)
	e.HashVersion = HashVersionLegacy
	sealed := e.Seal(GenesisHash)
	if sealed.HashVersion != HashVersionLegacy {
		t.Fatalf("hash version = %d", sealed.HashVersion)
	}
	if err := VerifyChain([]Entry{sealed}); err != nil {
		t.Fatal(err)
	}
}

// TestComputeHashCoversEveryField is the guarantee the chain rests on: if any
// field can be changed without changing the hash, that field can be tampered
// with undetectably.
func TestComputeHashCoversEveryField(t *testing.T) {
	t.Parallel()

	base := testEntry(1)
	baseHash := base.ComputeHash(GenesisHash)

	tests := []struct {
		field  string
		mutate func(*Entry)
	}{
		{"Seq", func(e *Entry) { e.Seq = 99 }},
		{"RunID", func(e *Entry) { e.RunID = "run_other" }},
		{"PrincipalID", func(e *Entry) { e.PrincipalID = "prn_other" }},
		{"Provider", func(e *Entry) { e.Provider = "anthropic" }},
		{"Model", func(e *Entry) { e.Model = "claude-sonnet-5" }},
		{"InputTokens", func(e *Entry) { e.InputTokens = 1001 }},
		{"OutputTokens", func(e *Entry) { e.OutputTokens = 501 }},
		{"Cost", func(e *Entry) { e.Cost++ }},
		{"Estimated", func(e *Entry) { e.Estimated = !e.Estimated }},
		{"CreatedAt", func(e *Entry) { e.CreatedAt = e.CreatedAt.Add(time.Nanosecond) }},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()

			mutated := base
			tt.mutate(&mutated)
			if got := mutated.ComputeHash(GenesisHash); got == baseHash {
				t.Errorf("changing %s did not change the hash; that field is not covered", tt.field)
			}
		})
	}

	t.Run("PrevHash", func(t *testing.T) {
		t.Parallel()

		other := strings.Repeat("a", 64)
		if got := base.ComputeHash(other); got == baseHash {
			t.Error("changing prevHash did not change the hash")
		}
	})
}

// TestComputeHashResistsFieldBoundaryForgery checks the reason fields are
// length-prefixed rather than concatenated. Without prefixes, moving a
// character across a field boundary would produce an identical digest.
func TestComputeHashResistsFieldBoundaryForgery(t *testing.T) {
	t.Parallel()

	a := testEntry(1)
	a.Provider, a.Model = "openai", "gpt-4o"

	b := testEntry(1)
	b.Provider, b.Model = "openaigpt", "-4o"

	if a.ComputeHash(GenesisHash) == b.ComputeHash(GenesisHash) {
		t.Error("two entries differing only in field boundaries hashed identically")
	}
}

// TestComputeHashIgnoresTimeZone guards against the same instant hashing
// differently depending on the server's local zone.
func TestComputeHashIgnoresTimeZone(t *testing.T) {
	t.Parallel()

	utc := testEntry(1)
	utc.CreatedAt = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	elsewhere := testEntry(1)
	elsewhere.CreatedAt = utc.CreatedAt.In(time.FixedZone("UTC+8", 8*60*60))

	if utc.ComputeHash(GenesisHash) != elsewhere.ComputeHash(GenesisHash) {
		t.Error("the same instant hashed differently in a different time zone")
	}
}

func TestSeal(t *testing.T) {
	t.Parallel()

	e := testEntry(1).Seal(GenesisHash)
	if e.PrevHash != GenesisHash {
		t.Errorf("PrevHash = %q, want the genesis hash", e.PrevHash)
	}
	if e.Hash == "" {
		t.Fatal("Seal left Hash empty")
	}
	if len(e.Hash) != 64 {
		t.Errorf("Hash length = %d, want 64 hex characters", len(e.Hash))
	}
	if e.Hash != e.ComputeHash(e.PrevHash) {
		t.Error("sealed Hash does not match recomputation")
	}
}

func TestVerifyChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		build    func() []Entry
		wantErr  bool
		wantSeq  int64
		contains string
	}{
		{
			name:  "empty chain is valid",
			build: func() []Entry { return nil },
		},
		{
			name:  "single entry",
			build: func() []Entry { return buildChain(1) },
		},
		{
			name:  "many entries",
			build: func() []Entry { return buildChain(50) },
		},
		{
			name: "first entry not chained to genesis",
			build: func() []Entry {
				c := buildChain(3)
				c[0] = testEntry(1).Seal(strings.Repeat("f", 64))
				return c
			},
			wantErr:  true,
			wantSeq:  1,
			contains: "prev_hash",
		},
		{
			name: "a middle entry is edited",
			build: func() []Entry {
				c := buildChain(5)
				// Tamper with the cost without re-sealing: exactly what an
				// attacker with database access would try.
				c[2].Cost = money.MustParseUSD("0.01")
				return c
			},
			wantErr:  true,
			wantSeq:  3,
			contains: "stored hash",
		},
		{
			name: "an entry is removed from the middle",
			build: func() []Entry {
				c := buildChain(5)
				return append(c[:2], c[3:]...)
			},
			wantErr:  true,
			wantSeq:  4,
			contains: "prev_hash",
		},
		{
			name: "entries are reordered",
			build: func() []Entry {
				c := buildChain(4)
				c[1], c[2] = c[2], c[1]
				return c
			},
			wantErr:  true,
			wantSeq:  3,
			contains: "prev_hash",
		},
		{
			name: "an entry is appended without sealing",
			build: func() []Entry {
				c := buildChain(3)
				return append(c, testEntry(4))
			},
			wantErr:  true,
			wantSeq:  4,
			contains: "prev_hash",
		},
		{
			name: "a re-sealed forgery still breaks the following entry",
			build: func() []Entry {
				c := buildChain(4)
				// Change an entry and re-seal it properly. The entry itself
				// now verifies, but its successor's PrevHash no longer matches.
				c[1].Cost = money.MustParseUSD("100.00")
				c[1] = c[1].Seal(c[0].Hash)
				return c
			},
			wantErr:  true,
			wantSeq:  3,
			contains: "prev_hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := VerifyChain(tt.build())

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("VerifyChain() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("VerifyChain() = nil, want an error")
			}

			var ce *ChainError
			if !errors.As(err, &ce) {
				t.Fatalf("error type = %T, want *ChainError", err)
			}
			if ce.Seq != tt.wantSeq {
				t.Errorf("failed at seq %d, want %d (%v)", ce.Seq, tt.wantSeq, err)
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q does not mention %q", err, tt.contains)
			}
		})
	}
}

// TestChainErrorNamesTheRow guards the operator experience: a broken chain is
// a serious event, and the message has to say which row to go and look at.
func TestChainErrorNamesTheRow(t *testing.T) {
	t.Parallel()

	c := buildChain(3)
	c[1].Model = "tampered"

	err := VerifyChain(c)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "seq 2") {
		t.Errorf("error %q does not identify the offending row", err)
	}
}
