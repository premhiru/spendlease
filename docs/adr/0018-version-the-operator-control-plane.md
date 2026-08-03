# 18. Version the operator control plane

- **Status:** Accepted
- **Date:** 2026-08-03

## Context

The local CLI is useful for bootstrapping one gateway, but an orchestrator
needs to create and close runs, issue and revoke individual leases, and check
remaining budget without opening the SQLite file or parsing human-readable
command output. Reusing dashboard form endpoints would couple clients to HTML
fragments and would not cover those resources.

The control surface carries more authority than an agent lease. It must not
turn the vendor-facing proxy into a public management endpoint, expose stored
token hashes, or introduce floating-point money values.

## Decision

Expose a guarded JSON control plane under `/api/v1`. It shares the dashboard's
loopback-or-admin-token authorization policy and requires the existing
`X-Spendlease-Admin: 1` mutation header on `POST` requests. Requests reject
unknown fields and have a 1 MiB body limit.

Represent every USD value as a decimal string and every time as RFC 3339.
Return the plaintext lease token only from the issue operation; list and
revocation responses contain metadata only. Make single-lease revocation use
the same memory-first invalidation order as the principal kill switch.

Treat ledger verification and export as operator operations. JSON and CSV
exports include the hash-chain fields and allow run, principal, and time
filters. The URL version covers the HTTP contract; breaking changes require a
new version rather than silently changing `/api/v1`.

## Consequences

- Orchestrators have a stable machine-readable lifecycle without database
  access.
- Local scripts need no admin token, while remote callers use the same token
  and network controls as the dashboard.
- SDK admin clients can remain thin wrappers around documented HTTP.
- The API is intentionally single-tenant and has one administrative trust
  level; scoped RBAC and user accounts remain out of scope.
- Closing a run and issuing a lease are enforced by the store, not only by an
  HTTP pre-check, so concurrent operations cannot bypass the closed state.
