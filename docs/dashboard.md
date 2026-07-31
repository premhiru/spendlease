# Dashboard

The dashboard is served at the gateway root, usually
<http://localhost:4000>. It is a current operational summary, not an invoice
or historical reporting interface.

## Table fields

Rows are sorted by recorded spend, highest first.

| Column | Meaning |
|---|---|
| Agent | Principal name and `prn_...` ID. |
| Mode | Current `observe` or `enforce` policy. Click to switch modes. |
| Status | Whether the principal has an active lease, only revoked leases, only expired leases, or no leases yet. |
| Leases | Counts of active, revoked, and expired credentials. |
| Runs | Number of runs owned by the principal. |
| Calls | Settled ledger entries. A `~N` marker means N entries used estimated token usage or fallback pricing. |
| Blocked | Requests rejected before egress. Observe-mode would-block decisions are shown separately. |
| Spend | Sum of calculated token cost across the principal's runs. |
| Last event | Most recent allowed call, budget decision, revocation, or expiration. |
| Kill | Revokes every active lease for the principal. |

The table refreshes every three seconds. Refresh pauses while a button has
focus so a control is not replaced during a click.

## Observe-mode badge

`would have been blocked` appears when a run exceeded its budget while the
principal was in observe mode. The request was forwarded and settled; the
badge shows what enforcement would have rejected.

Before switching to `enforce`, compare the dashboard with the vendor's own
usage and billing data. The spend column covers the price-book token charges
documented under [Price book](pricing-book.md#what-is-not-modeled), not every
possible vendor fee.

## Controls

Clicking the mode value alternates between `observe` and `enforce`. It calls:

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
guard as the rest of the dashboard.

## Recent events

The most recent operational events appear below the summary. Successful calls
come from the append-only ledger. Budget blocks are stored separately because
a rejected request has no cost and must not become a ledger entry. Revoked and
expired events come from the durable lease records.

This makes an enforced `402 budget_exceeded` visible without charging the
vendor or requiring access to server logs.

## Remote access

Loopback access does not require a credential. Non-loopback access is refused
unless `SPENDLEASE_ADMIN_TOKEN` or `--admin-token` is configured. Browsers use
HTTP Basic authentication with any username and the token as the password.

Place TLS and a network access policy in front of a remotely reachable
dashboard. See [Self-hosting](self-hosting.md#dashboard-and-admin-access).

## Current limitations

The dashboard does not provide charts, per-run drill-down, ledger export, or
user accounts. The recent-events table is an operational view, not an invoice
or full audit export. There is also no operator CLI for verifying or exporting
the hash chain in v0.1; `ledger.VerifyChain` is currently a Go library function.
