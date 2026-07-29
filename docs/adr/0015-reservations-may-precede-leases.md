# ADR-0015: Reservations may precede leases

**Status:** Accepted

## Context

The phased plan deliberately lands reserve/settle before lease issuance. The
existing schema nevertheless requires every reservation to reference a lease.
During phase 7, requests still authenticate with a principal key, so no honest
lease exists to reference.

## Decision

`reservations.lease_id` is nullable. A phase-7 request authenticated by a
principal key leaves it null. Once lease authentication lands, reservations
created from a lease carry its real identifier.

Null means exactly “authorized through the temporary principal-key path”; it
does not mean an unknown or deleted lease. Existing foreign-key enforcement
still applies whenever an identifier is present.

## Consequences

- Reserve/settle can ship independently without fabricating security objects.
- Phase 8 can attach real lease attribution without another reservation model.
- Principal-key authentication remains visibly transitional and can be removed
  after lease onboarding is complete.

## Rejected alternatives

**Create an immortal synthetic lease for every implicit run.** That would put
fake credentials and fake expiry into a security-sensitive table, and later
code could accidentally treat them as genuine authorization.

**Move lease issuance into phase 7.** That bundles two independently
reviewable authorization changes and violates the repository's phased plan.

**Keep the foreign key non-null and store an empty identifier.** An empty
string is still a foreign-key value and either violates integrity or requires
a fake referenced row.
