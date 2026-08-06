# Dashboard

The dashboard is served at the gateway root, usually
<http://localhost:4000>. It is a current operational summary, not an invoice
or historical reporting interface.

The header identifies the running build, enforcement policy, and exact active
price-book revision. A locally compiled binary says `Local development build`; release
builds show their version. The price-book label includes the first eight
characters of a SHA-256 revision and the newest active effective date. Its
detail text gives the load time, provider count, and canonical price-entry
count. The count describes the loaded snapshot, not every model currently sold
by every vendor and not aliases that resolve to an entry.

A separate verification line shows the oldest vendor-review date among active
entries. It is highlighted when evidence is missing or more than 45 days old.
Use `spendlease pricing verify` for the provider-level report; freshness means
the checked date is current, not that Spendlease scraped or inferred a vendor
price automatically.

## Table fields

Rows are sorted by recorded spend, highest first.

| Column | Meaning |
|---|---|
| Agent | Principal name and `prn_...` ID. |
| Mode | `observe`, or the running enforcement policy (`strict` or `best-effort`). Click to switch the persisted principal mode. |
| Status | Whether the principal has an active lease, only revoked leases, only expired leases, or no leases yet. |
| Leases | Counts of active, revoked, and expired credentials. |
| Runs | Number of runs owned by the principal. |
| Calls | Settled ledger entries. A `~N` marker means N entries used estimated token usage or fallback pricing. |
| Blocked | Requests rejected before egress. Observe-mode would-block decisions are shown separately. |
| Spend | Sum of calculated token cost across the principal's runs. |
| Last event | Most recent allowed call, budget decision, revocation, or expiration. |
| Actions | Opens run and lease management. Admins can also revoke every active lease for the principal. |

The table refreshes every three seconds. Refresh pauses while a button has
focus so a control is not replaced during a click.

## Observe-mode badge

`would have been blocked` appears when a run exceeded its budget while the
principal was in observe mode. The request was forwarded and settled; the
badge shows what enforcement would have rejected.

Before switching to enforcement, compare the dashboard with the vendor's own
usage and billing data. The spend column covers the price-book token charges
documented under [Price book](pricing-book.md#what-is-not-modeled), not every
possible vendor fee.

## Add an agent

Admins can create an agent without assembling several CLI commands. **Add an
agent** asks for a unique name, mode, positive run budget, lease duration, and
one or more allowed providers.

The principal, root run, and first lease are committed in one datastore
transaction. A failure does not leave a half-created agent. The resulting
`sll_...` lease appears once, beside the provider base URLs. Only its hash is
stored, so closing or refreshing the result cannot reveal it again.

The same one-time response contains copy buttons for environment values,
dependency installation, Python, JavaScript, and `curl`. Each selected
provider gets its own model and base URL. Anthropic examples use the native
Anthropic client and `x-api-key`; the other six use their OpenAI-compatible
clients and bearer authentication. Every request has an explicit output
ceiling, so it works with the default strict enforcement policy.

Dashboard-created principals deliberately do not expose their long-lived
`slk_...` compatibility key. Applications should use leases. The CLI remains
available for a legacy integration that still requires a principal key.

## Provider keys

The admin-only **Provider keys** panel reports whether each routed provider is
configured. An admin can store, replace, or remove a key. Submitted keys go
straight to the encrypted credential vault and are not included in the HTML
response, logs, or later status reads.

Removing a key does not revoke leases. Requests scoped to that provider return
`503 provider_credential_missing` until a replacement is stored.

## Run and lease controls

**Manage** opens one agent's runs and lease metadata. An operator or admin can
create a root run with a positive budget, issue a provider-scoped lease with
an optional lower ceiling, revoke one active lease, or close a run. Closing a
run prevents every lease on that run from authorizing more spend.

New lease tokens are shown once. Existing lease rows contain identifiers,
scope, ceilings, expiry, and status, never plaintext tokens.
Freshly issued leases include the same provider-specific copyable examples as
the initial agent workflow.

## Mode and kill controls

Clicking the mode value alternates between `observe` and the running
enforcement policy. The persisted API value remains `enforce`; the dashboard
shows whether that means `strict` or `best-effort` for this server. It calls:

```text
POST /admin/principals/{id}/mode
```

Clicking **Revoke** invalidates every current lease for that principal. It does
not stop a request that has already reached the vendor, delete the principal,
or prevent a new lease from being issued later.

The response confirms how many active leases were revoked. The row then shows
`Revoked` when no active lease remains, and the revocations stay visible in
the recent-events table.

Both controls return an updated table fragment and use the same authorization
guard as the rest of the dashboard. Mode changes and principal-wide revocation
require `admin`; run and individual-lease management require `operator` or
`admin`.

## Recent events

The most recent operational events appear below the summary. Successful calls
come from the append-only ledger. Budget blocks are stored separately because
a rejected request has no cost and must not become a ledger entry. Revoked and
expired events come from the durable lease records.

This makes an enforced `402 budget_exceeded` visible without charging the
vendor or requiring access to server logs.

The default view shows events that need attention from the last 24 hours:
budget blocks, observe-mode would-block decisions, revocations, and
expirations. Successful calls remain available under **All results** or
**Allowed**.

Filters run in the datastore before rows are limited, so they can find an
older match rather than searching only what is already visible. Events can be
narrowed by:

- agent;
- result;
- the last hour, 24 hours, seven days, or all time; and
- a full or partial run or lease ID.

**Load 20 more** expands the current filtered result, up to 200 rows. The
summary table above is never changed by event filters.

## Remote access

Credential-free access requires both a loopback TCP peer and loopback HTTP
host. Non-local access uses a named `slo_` operator token. In the browser,
enter the operator name and token in the HTTP Basic prompt. The header shows
the current name and role. Viewers can inspect spend and events; operators can
manage runs and individual leases; admins can also create agents, manage
provider keys, switch mode, and use the principal-wide kill switch. Mutations
require the dashboard's anti-CSRF header and a same-origin browser request.

The older `SPENDLEASE_ADMIN_TOKEN` and `--admin-token` credential remains a
temporary `legacy-admin` migration path.

Place TLS and a network access policy in front of a remotely reachable
dashboard. See [Self-hosting](self-hosting.md#dashboard-and-admin-access).

## Current limitations

The dashboard does not provide charts or historical spend reports. Its run
workspace is for access management, not per-run accounting analysis.
The recent-events table is an operational view capped at 200 matching rows,
not an invoice or full audit export. Use `spendlease ledger export` or the
guarded `/api/v1/ledger/export` endpoint for the complete filtered data, and
use the matching verify command or endpoint to check the hash chain. Operator
control changes have a separate append-only trail available through
`spendlease keys operator audit`.
