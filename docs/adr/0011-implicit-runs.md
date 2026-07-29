# 11. Every principal has an implicit run

- **Status:** Accepted
- **Date:** 2026-07-29

## Context

Spend is charged to a run, and the ledger enforces that with a foreign key: there is no such thing as an entry belonging to no run. But runs are issued with leases, which do not exist yet, so at the moment nothing creates one.

Something has to give. The options were to make `run_id` nullable, to refuse requests until a run is created, or to create one automatically.

## Decision

**A request without a run is charged to its principal's implicit run**, created on first use.

A caller that already models executions can name one instead, with the `X-Spendlease-Run` header. Naming a run that does not exist, or one belonging to a different principal, is a `400` — resolved during authentication, before the request is forwarded, so the caller finds out immediately rather than having the call succeed and the spend land somewhere unexpected.

The implicit run's identifier is **derived from the principal's**: `prn_abc…` gets `run_abc…`. That makes it stable without a lookup table, obviously related when it turns up in a log line, and idempotent to create — two concurrent first requests race, the loser sees a conflict, and a conflict means the run already exists, which is all the caller needed.

It is given a configurable budget (`--default-run-budget`, $10 by default). Nothing enforces it yet. It is recorded so the dashboard has something to measure spend against and so enforcement has a value to read when it lands.

## Consequences

- Attribution works from the first request, with no setup. That matters for observe mode, whose entire value proposition is that installing it costs nothing.
- A principal that never names a run accumulates all of its spend in one long-lived run. That is the correct shape for a long-running service and the wrong shape for discrete tasks, which is exactly why the header exists and why lease-issued runs are the eventual answer.
- The derived identifier means a user could in principle create a run whose ID collides with an implicit one. Run identifiers are 128 random bits, so this requires deliberate effort, and the result would be an ordinary run owned by the same principal.
- The implicit run is resolved on every request, which is one indexed lookup. If that ever shows up in a profile it can be cached against the principal, since the mapping never changes.

## Options rejected

- **Make `run_id` nullable.** Removes the problem by removing the guarantee. "Which run was this?" becoming answerable-or-not is precisely the ambiguity the four-object model exists to prevent, and retrofitting the constraint later would mean backfilling every historical row.
- **Refuse requests until a run exists.** Correct, and it would make observe mode require setup before it records anything — destroying the near-zero install cost that is the whole adoption argument.
- **Create a new run per request.** Faithful to "a run is one execution", and it would produce a run per HTTP call, making per-run budgets meaningless and the dashboard unreadable.
- **Infer runs from a session or connection.** No reliable signal exists across SDKs, and guessing wrongly would scatter one agent's spend across many runs or merge several agents into one.
