# ADR-0024: Itemized usage is part of the ledger hash

**Status:** Accepted; amends ADR-0005

## Context

The gateway prices uncached input, cache hits, two cache-write lifetimes, and
output separately. The original ledger retained only aggregate input and
output totals. Its cost was immutable, but an independent reviewer could not
reproduce a cached request's calculation from the row alone.

Vendor statements add dimensions on their own schedule. A ledger schema with
one database column per possible unit would require a migration for every new
billing surface, while a floating-point quantity would discard the exactness
used everywhere else in the project.

## Decision

New ledger entries use hash version 2. The hash covers a canonical JSON object
of lowercase named, non-negative integer usage dimensions, the upstream
request identifier when available, the active price-book revision, and the
effective date of the resolved price. Aggregate input/output columns remain
for compatibility and fast summaries.

Version 1 serialization remains unchanged. Migrations add new columns with
version-1 defaults, so an existing chain verifies byte-for-byte after upgrade.
Every new append uses version 2 and both database backends keep rejecting
updates and deletes.

Reconciliation accepts a documented normalized CSV rather than embedding
vendor-specific invoice clients. It aggregates both sides by provider/model
over a half-open UTC interval, compares exact USD nanodollars and named integer
usage, and never mutates the ledger. Arbitrary statement units are reportable
even when they are not enforceable.

## Consequences

- Cached-token and cache-write costs can be reproduced from a new ledger row.
- Price-book provenance and upstream IDs survive normal export and backup.
- New whole-count billing units do not require a ledger schema migration.
- Old rows retain aggregate token usage only; missing historical detail cannot
  be recreated.
- A vendor export must be normalized before comparison, and that conversion
  should be retained as audit evidence.
- Representing a unit in reconciliation does not certify it for pre-egress
  budget enforcement. Unsupported spend still fails closed in enforce mode.

## Rejected alternatives

**Rewrite old hashes.** That would destroy the ledger's continuity at upgrade.

**One column per billing unit.** Provider billing changes too often for the
database schema to be the extension mechanism.

**Direct vendor billing APIs in the gateway.** They add broad read credentials,
network dependencies, pagination state, and vendor-specific release cadence to
the enforcement process. Normalization keeps reconciliation offline and
reviewable.
