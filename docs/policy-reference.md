# Policy reference

The v0.1 policy surface is deliberately small. A principal has one mode; runs
carry budgets; the active price book supplies reservation defaults. There is
no general rule language or capability system.

## Principal mode

| Value | Default | Behaviour |
|---|---:|---|
| `observe` | yes | Price, reserve, settle and log every request, but never reject for budget. Would-block decisions remain visible. |
| `enforce` | no | Reject a reservation that does not fit with `402 budget_exceeded`. Datastore decision failures fail closed. |

Change mode from the dashboard, with the CLI:

```bash
spendlease keys principal set-mode checkout-agent --mode enforce
```

or through `POST /admin/principals/{id}/mode`. The admin guard described in
[self-hosting](self-hosting.md#dashboard-and-admin-access) applies.

Mode is read when the request authenticates. An already-authorized in-flight
request is settled normally if the mode changes before its response finishes.

## Run budget

Budgets are exact US-dollar amounts stored as integer nanodollars.

- `0` means no configured ceiling, not zero permission to spend.
- A request counts settled ledger spend and every pending reservation.
- A child is checked against its own budget and every budgeted ancestor.
- Descendant spend and holds roll up to the ancestor, so siblings share the
  parent's remaining balance.
- Exact exhaustion is allowed; the next positive reservation is rejected.

Until explicit run creation lands with leases, requests without
`X-Spendlease-Run` use the principal's implicit run. Its budget defaults to
`$10.00` and is configured at startup:

```bash
spendlease serve --default-run-budget 25.00
```

## Reservation defaults

These are gateway lifecycle settings rather than per-principal policy:

| Flag | Default | Meaning |
|---|---:|---|
| `--reservation-ttl` | `15m` | Maximum lifetime of an in-flight hold before it may be reclaimed. |
| `--reservation-sweep-interval` | `30s` | How often expired pending holds are reclaimed. |

The price book's model-level `default_max_tokens` is used when a request omits
its output ceiling. Unknown models use the configured fallback input/output
rates and fallback ceiling; they are never treated as free.

## Deliberate omissions

There is no per-endpoint policy, time-of-day policy, approval workflow,
multi-currency rule, RBAC, anomaly detector or general expression language in
v0.1. Provider scope and per-lease ceilings arrive with leases; they do not
expand the principal policy into a generic capability system.
