# 5. How ledger immutability is actually enforced

- **Status:** Accepted
- **Date:** 2026-07-28

## Context

"The ledger is append-only and tamper-evident" is a claim, and claims about integrity are worth exactly as much as their enforcement. Three separate questions hide inside it: what stops a write, what makes tampering detectable, and what stops two concurrent appends forking the chain.

## Decision

### Immutability is enforced by the database, not by application code

`migrations/0001_init.sql` creates `BEFORE UPDATE` and `BEFORE DELETE` triggers on `ledger` that `RAISE(ABORT, 'ledger is append-only: ...')`.

Enforcing this in Go would only bind code that goes through the store. A trigger also binds a future migration, a background job written in a hurry, and an operator at a `sqlite3` prompt. The store surfaces the failure as `store.ErrImmutable` so callers can recognise it, but the refusal itself happens below any code we might get wrong.

Correcting a mistake means appending a compensating entry, exactly as a paper ledger would.

### Hashing is length-prefixed, not concatenated

Each entry's hash covers every field including the previous entry's hash. Fields are serialised as `len:value`, not joined with a separator.

Concatenation is forgeable. With a separator or none at all, `provider="openai", model="gpt-4o"` and `provider="openaigpt", model="-4o"` produce the same byte stream and therefore the same digest, so one entry could be substituted for another without breaking the chain. Length prefixes make the field boundaries part of the hashed input. There is a test that specifically asserts these two entries hash differently.

Timestamps are normalised to UTC before hashing, so the same instant does not hash differently depending on the server's local zone.

### Chain verification recomputes, it does not just compare

`ledger.VerifyChain` checks that each entry's `PrevHash` matches its predecessor's `Hash` **and** that each entry's stored `Hash` still matches a fresh computation over its contents.

The second check is what makes it a tamper detector rather than an ordering check. Without it, editing a row's cost while leaving its hash alone would pass. With it, that edit invalidates the row and every row after it. Verification failures name the sequence number, because "the ledger is broken" without a row number is not an actionable alert.

### Appends are serialised by a mutex, backed by database constraints

Sealing an entry requires reading the current head hash and inserting the successor atomically with respect to other appends. SQLite gives a single writer, but two goroutines can still read the same head before either writes.

`sqlite.Store` holds a mutex across the read-seal-insert transaction. This is correct because spendlease is deployed as one process against one database — a single container, which is the whole distribution model.

If two processes ever did share a database file, the mutex would not span them. That case degrades safely rather than silently: `seq` is the primary key and `hash` is unique, so the loser of the race gets a failed insert instead of a forked chain. A corrupt ledger is unacceptable; a failed write that the caller can retry is not.

## Consequences

- Spend history cannot be rewritten through any path, including ones that bypass this codebase.
- Tampering is detectable after the fact, and the detector points at the offending row.
- Ledger append throughput is bounded by one writer. This is not a concern: appends happen once per completed request, not per streamed chunk.
- The PostgreSQL backend must reproduce all of this. Triggers and a unique index translate directly; the mutex should become `SELECT ... FOR UPDATE` on the head row, since multiple processes against one PostgreSQL instance is a real deployment.
- Verification is O(n) over the entries checked. A periodic verification job over a large ledger will need a checkpointing strategy; that is not needed yet and is not built.

## Options rejected

- **Enforce append-only in the store layer only.** Cheaper, and worthless against exactly the access paths that matter during an incident.
- **Sign entries with a private key instead of chaining hashes.** Stronger against an attacker who can rewrite the whole table, but introduces key management, key rotation, and a new secret to protect — for a threat model where the database and the key would sit on the same host anyway. Chaining is the right cost for the guarantee.
- **Merkle tree instead of a linear chain.** Enables efficient proofs about subsets, which nothing in this product needs. A linear chain is simpler to reason about and to verify by hand.
- **Serialise appends with `BEGIN IMMEDIATE` and no mutex.** Would work, but pushes correctness onto DSN configuration that is easy to change accidentally, and gives worse errors under contention than an in-process lock.
