# ADR-0016: Revocation is memory-first and durable

**Status:** Accepted

## Context

The kill switch must stop a live agent in under one second, while revocation
must also survive a restart.

## Decision

The gateway keeps revoked lease token hashes in a concurrency-safe in-memory
set checked before datastore lookup on every lease request. A principal kill
adds all current lease hashes to that set, then records revocation durably.
After restart, the durable `revoked_at` field remains authoritative.

## Consequences

The live path fails closed immediately, restarts preserve the kill, and only
hashes—not plaintext tokens—are retained.

## Rejected alternatives

Database-only revocation couples emergency-stop latency to storage. Memory-only
revocation silently resurrects leases after restart.
