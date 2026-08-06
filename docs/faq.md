# FAQ

## Is spendlease ready for production?

It is pre-v1. The current beta,
[`v0.2.0-beta.2`](https://github.com/premhiru/spendlease/releases/tag/v0.2.0-beta.2),
is the first release intended for an end-to-end evaluation. It includes the
encrypted vault, datastore-backed ledger, reserve/settle enforcement,
short-lived leases, versioned operator API, and immediate kill switch. Pin the
release digest or commit and evaluate it in observe mode before making it a
production dependency. The beta, current `main`, and `edge` default to strict
enforcement; best-effort estimates require an explicit server option.

## Is the configured budget a ceiling on my vendor invoice?

Not in every billing scenario. The gateway enforces the token rates in its
price book and rejects explicit premium or ambiguous processing modifiers
before egress. It cannot inspect account-level defaults or private contracts,
and batch, persistent cache storage, tool fees, and non-token charges are not
modeled. Some omissions overcount and some undercount. Compare the ledger with
the vendor bill in observe mode for the models and features your workload uses.

## Why does a new principal default to observe mode?

An unproven gateway should not enter the blocking path on day one. Observe
mode performs the same pricing and budget decisions but forwards requests that
would have been blocked. Once the dashboard matches your vendor bill and
workload expectations, switch the principal to enforce mode.

## What happens when `max_tokens` is missing?

Strict enforcement returns `422 spend_not_enforceable` before contacting the
vendor. Set `max_tokens`, `max_completion_tokens`, or `max_output_tokens`, as
accepted by the provider endpoint. Observe mode and explicitly enabled
best-effort enforcement use the active price-book entry's
`default_max_tokens`, which is a practical estimate rather than a guaranteed
vendor output limit.

## Why did I receive `402 Payment Required`?

The request's upper-bound reservation did not fit the target run or one of its
budgeted ancestors. The JSON response names that run and includes its budget,
settled spend, pending holds, requested amount, remaining balance and
shortfall. Reduce the output ceiling, increase the run budget, or use observe
mode while validating the estimate.

## Are provider failures charged?

No. A non-2xx response or transport failure releases the full reservation and
does not append a ledger entry. A client disconnect is different: the vendor
may already have done billable work, so spendlease settles the usage observed
before the disconnect or records a marked estimate.

## What does `422 spend_not_enforceable` mean?

The request uses an endpoint, feature, or body shape whose potential vendor
charge cannot be bounded by the token price book. This includes an unknown
model, a missing output limit, explicit premium processing, oversized or
unparseable bodies, media, batches, and provider-hosted tools. Strict
enforcement refuses it before the vendor is contacted. Observe mode can pass
it through, but marks the response `X-Spendlease-Accounting: unmetered` and
does not add a misleading token charge to the ledger.

## Does the dashboard need internet access?

No. Its HTML, CSS and htmx are embedded in the binary. Loopback access is
credential-free; remote access requires a named operator token and should be placed
behind TLS.

## Can I export or verify the ledger from the CLI?

Yes. Run `spendlease ledger verify` to check the complete chain and
`spendlease ledger export --format json|csv` to export it. The guarded JSON API
and both SDK admin clients expose the same operations. Back up the datastore
as described under [Self-hosting](self-hosting.md#backups); the hash
chain is not a substitute for a backup.

## Is PostgreSQL supported?

Yes. Pass a `postgres://` or `postgresql://` DSN to every command through
`--store`. PostgreSQL serializes schema migrations, budget decisions, and
ledger appends across gateway instances. SQLite remains the default for a
single-process installation and needs no database server. See
[Self-hosting](self-hosting.md#postgresql) for setup and operational guidance.

## Can the master key come from a secret manager or KMS?

Yes. Supply it directly, mount it as a file, or configure a no-shell command
that prints it. The file path works with common container and Kubernetes secret
mounts; the command can call an operator-owned secret-manager or KMS wrapper
using workload identity. Master-key rotation uses a temporary previous-key
fallback and re-encrypts the complete credential vault in one transaction.
Follow the staged procedure under
[Self-hosting](self-hosting.md#rotate-the-master-key).

## Can several people administer one gateway safely?

Yes. Give each person a named operator token and the narrowest useful role:
`viewer`, `operator`, or `admin`. Tokens are shown once and stored only as
hashes. HTTP mutations leave an append-only attempt and result trail, so a
shared credential is not needed. The old `SPENDLEASE_ADMIN_TOKEN` remains only
to make upgrades possible and should be removed after clients migrate.

## How should I monitor a production gateway?

Probe `/healthz` for liveness and `/readyz` for datastore readiness. Scrape
`/metrics` with Prometheus for aggregate request, latency, byte, budget, and
alert-delivery counters. For immediate budget and upstream failures, configure
the signed alert webhook. These surfaces intentionally omit prompts,
credentials, model names, and per-agent labels.

## Why no LangChain or CrewAI integration?

Base-URL override works with vendor SDKs in every language and does not depend
on a framework's release cycle. Framework-specific adapters are deliberately
out of scope for the first beta.
