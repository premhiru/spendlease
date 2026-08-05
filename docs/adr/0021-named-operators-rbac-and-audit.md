# ADR-0021: Named operators, roles, and append-only control audit

**Status:** Accepted

**Amends:** ADR-0018. The `/api/v1` routes and mutation header remain; this
record replaces anonymous shared administration as the production model.

## Context

A shared admin secret can prove that a caller knew one value, but it cannot say
which person or automation made a change. It also grants read access, routine
run management, enforcement changes, and the kill switch as one indivisible
permission. That is workable for a localhost demo and too coarse for a team.

Control-plane logs are not enough for incident review. They may be sampled,
rotated, or shipped independently of the database whose state changed. A failed
audit write must not quietly allow a mutation to proceed.

## Decision

Named operators authenticate with random `slo_` tokens. Only a SHA-256 digest
is stored. The roles are cumulative:

- `viewer` reads the dashboard, budgets, leases, and spend ledger;
- `operator` also creates and closes runs and issues or revokes leases;
- `admin` also changes enforcement, activates the principal kill switch, and
  reads the operator audit endpoint.

The local CLI creates, lists, rotates, revokes, and changes roles. It refuses to
disable or demote the final active admin. SQLite serializes that check in the
process; PostgreSQL adds a transaction-scoped advisory lock across instances.

For every authenticated HTTP mutation, the guard appends an `attempt` record
before calling the handler and a `result` record afterward. An attempt-write
failure returns `503` without invoking the handler. The two records share a
request ID and include actor, role, action, resource, peer address, timestamp,
and final HTTP status. Database triggers reject audit updates and deletes.

Loopback retains credential-free admin access and records the actor as
`local`. The old shared token remains temporarily as `legacy-admin`, with a
startup warning, so an existing remote deployment can migrate without an
outage.

## Consequences

- Teams can give monitoring systems read-only access and orchestrators routine
  run authority without granting the kill switch.
- Token rotation invalidates the old credential immediately and does not
  change the operator identity used in later records.
- Audit attempt durability is a prerequisite for a mutation. If the result
  write fails after a handler completes, the attempt remains as an explicitly
  uncertain operation and the failure is logged.
- Tokens are bearer credentials, not transport encryption. TLS and network
  policy remain deployment requirements.
- Operator lifecycle changes made through the local CLI are attributed to
  `local-cli`; operating-system access controls remain the identity boundary
  for local administration.

## Rejected alternatives

**One shared admin token.** It has no attribution and cannot express least
privilege.

**Human passwords.** Password storage needs a slow password KDF and an account
recovery flow. High-entropy generated bearer tokens match the existing
principal and lease credential model.

**Audit only in application logs.** Logs are useful operational evidence but
do not provide a datastore-enforced append-only history beside the controlled
state.

**One mutable row per request.** Updating `attempt` into `result` creates an
audit mutation surface and loses the evidence of uncertain outcomes.
