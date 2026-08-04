# ADR-0019: PostgreSQL is the multi-instance storage backend

**Status:** Accepted

**Amends:** ADR-0005 and ADR-0014 for the PostgreSQL backend. Their SQLite
decisions remain unchanged.

## Context

SQLite makes the first deployment unusually easy, but its correctness model is
one gateway process and one local database file. A production deployment may
need multiple gateway replicas, managed backup and failover, and a database
that is not tied to one container's filesystem. Adding PostgreSQL is only
useful if it preserves the existing authorization and ledger guarantees under
cross-process concurrency.

## Decision

The `--store` flag or `SPENDLEASE_STORE` selects PostgreSQL when its value
begins with `postgres://` or `postgresql://`; every other value remains a SQLite path. Both adapters
implement the same store and credential-vault contracts, use the same RFC 3339
timestamp representation, and store money as integer nanodollars.

PostgreSQL migrations are embedded in the binary and applied in one
transaction protected by a transaction-scoped advisory lock. Concurrent
replicas can start against an empty or partially upgraded database without
both applying a migration.

Budget reservation decisions take a transaction-scoped advisory lock derived
from the principal ID before reading hierarchy usage and inserting a hold.
Every run in one hierarchy belongs to that principal, so parent and descendant
budgets are serialized together while unrelated principals remain parallel.
Closing a run takes the same lock, preventing an authorization from racing a
close operation.

Ledger append and reservation settlement take a separate global
transaction-scoped advisory lock before reading the ledger head. This covers
the empty-ledger case and guarantees one successor across all replicas. The
database also enforces append-only rows through `BEFORE UPDATE` and `BEFORE
DELETE` triggers.

PostgreSQL always requires `SPENDLEASE_MASTER_KEY`. A DSN is not a safe or
meaningful location for the development key-file fallback used with SQLite.

## Consequences

- Multiple gateway processes can share budgets, leases, credentials, events,
  and one verifiable ledger.
- A hot principal's authorization decisions are serialized. That is an
  intentional correctness boundary; different principals do not block one
  another.
- Ledger writes are globally serialized because the hash chain itself has one
  global head.
- SQLite stays the default and gains no PostgreSQL runtime requirement.
- Operators must provision PostgreSQL, protect its DSN, size its connection
  limit across replicas, and run database-native backups.
- PostgreSQL and SQLite migrations are separate files. Integration tests must
  exercise behavior against a real PostgreSQL service, including concurrent
  startup, oversubscription attempts, ledger appends, and immutability.

## Rejected alternatives

**Share SQLite on a network filesystem.** SQLite's locking guarantees depend
on filesystem behavior and do not create a reliable multi-host service.

**Use only process mutexes.** They cannot coordinate replicas and would allow
two processes to authorize the same remaining budget or seal successors from
the same ledger head.

**Use a single global lock for all budget decisions.** Correct but needlessly
couples unrelated customers. Principal-scoped locks preserve hierarchy
correctness with useful parallelism.

**Lock only the current ledger head row.** There is no row to lock for the
first append. An advisory lock provides the same rule for empty and non-empty
ledgers.
