# Production checklist

Use this checklist after a successful observe-mode evaluation and before real
agents depend on spendlease for enforcement. The goal is not merely to start
the process; it is to know how it fails, recovers, and proves what it charged.

## Release and compatibility

- [ ] Pin a versioned binary, immutable container digest, or commit. Do not
  deploy `edge` as a production dependency.
- [ ] Verify checksums and release provenance before installation.
- [ ] Read the release notes and [upgrade guide](upgrading-to-beta.md).
- [ ] Keep the previous binary or digest available for rollback.
- [ ] Treat all pre-v1 CLI, API, and schema surfaces as subject to change.

## State and recovery

- [ ] Use SQLite only for one gateway process on durable local storage.
- [ ] Use PostgreSQL when several replicas must share budgets and leases.
- [ ] Back up the datastore on a schedule that matches your recovery target.
- [ ] Back up the master key separately from the datastore.
- [ ] Restore both into an isolated environment and run
  `spendlease ledger verify`.
- [ ] Confirm expired reservations are swept after restarts.

## Secrets and access

- [ ] Supply the master key from a mounted secret or a no-shell secret-manager
  command; do not rely on development auto-generation.
- [ ] Create named operators with the narrowest useful role: `viewer`,
  `operator`, or `admin`.
- [ ] Remove the legacy shared admin token after migration.
- [ ] Put TLS and a network access policy in front of every non-loopback
  dashboard or control-plane endpoint.
- [ ] Test provider-key and operator-token rotation.
- [ ] Confirm logs, crash reports, and alert payloads contain no prompts,
  headers, vendor keys, or lease tokens.

## Pricing and enforcement

- [ ] Run `spendlease pricing verify --max-age 45d` during deployment.
- [ ] Confirm every production model appears in `spendlease pricing list`.
- [ ] Use explicit output-token limits in every output-producing request.
- [ ] Review premium processing, tools, media, caching, batch work, and private
  pricing against [what the price book does not model](pricing-book.md#what-is-not-modeled).
- [ ] Run the real workload in `observe` mode and reconcile at least one
  representative billing period with the vendor statement.
- [ ] Switch one principal at a time to `enforce` and test a deliberate
  `402 budget_exceeded` before expanding the rollout.

## Reliability

- [ ] Probe `/healthz` for liveness and `/readyz` for datastore readiness.
- [ ] Scrape `/metrics` and alert on request failures, budget blocks,
  datastore failures, upstream latency, and failed alert delivery.
- [ ] Give the gateway bounded connect, header, body, and request timeouts.
- [ ] Size PostgreSQL connections as the per-process pool multiplied by the
  maximum replica count.
- [ ] Test a vendor timeout, client disconnect, gateway restart, datastore
  outage, and exhausted budget.
- [ ] Verify that retry policy will not repeat a non-idempotent request after
  an ambiguous upstream result.

## Audit and operations

- [ ] Retain structured logs with request IDs for the period required by your
  incident process.
- [ ] Schedule `spendlease ledger verify` and alert on any failure.
- [ ] Export and retain ledger data before the primary datastore reaches its
  retention limit.
- [ ] Review named-operator audit records and test the principal kill switch.
- [ ] Write down who can increase budgets, rotate secrets, revoke leases, and
  declare an accounting discrepancy.

## Go-live decision

Do not enable enforcement because every box is checked mechanically. Enable it
when the team can answer all four questions with evidence:

1. Does spendlease price the exact request shapes this workload uses?
2. Does its ledger agree closely enough with the vendor statement for the
   budget's purpose?
3. Will a datastore, vendor, or gateway failure fail in the direction the
   workload expects?
4. Can an operator restore service and explain the resulting accounting trail?

If the answer to the first two is uncertain, remain in `observe`. If the
answer to the last two is uncertain, keep the gateway out of a critical path.

Implementation details and exact flags live in [Self-hosting](self-hosting.md).
Project-wide 1.0 gates live in [v1 readiness](v1-readiness.md).
