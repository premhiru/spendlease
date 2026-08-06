# Policy reference

The first-beta policy surface is small: principals choose whether budgets block,
runs carry those budgets, leases restrict provider access and optional spend,
and the price book supplies reservation defaults.

## Principal mode

| Value | Default | Behavior |
|---|---:|---|
| `observe` | yes | Price, reserve and settle supported token spend, but never reject for budget. Unsupported billing shapes pass through as visibly unmetered traffic. |
| `enforce` | no | Apply the server's enforcement policy, reject a reservation that does not fit with `402 budget_exceeded`, and fail closed on datastore decision errors. |

Change mode from the dashboard, with the CLI:

```bash
spendlease keys principal set-mode --name checkout-agent --mode enforce
```

or through `POST /admin/principals/{id}/mode`. The admin guard described in
[self-hosting](self-hosting.md#dashboard-and-admin-access) applies.

Mode is read when the request authenticates. An already-authorized in-flight
request is settled normally if the mode changes before its response finishes.

## Enforcement policy

The server chooses how an `enforce` principal handles estimates:

| Value | Default | Behavior |
|---|---:|---|
| `strict` | yes | Require a model in the active price book and an explicit output-token limit for output-producing requests. Otherwise return `422 spend_not_enforceable` before egress. |
| `best-effort` | no | Allow unknown-model fallback prices and the price book's `default_max_tokens` when the request supplies no limit. Budget decisions still block, but the reservation is an estimate rather than a bound for the modeled token rates. |

Choose the policy at startup:

```bash
spendlease serve --enforcement-policy=strict
```

The dashboard names the running policy. Use `best-effort` only when keeping a
new or unusual model available matters more than a strict upper bound.

## Run budget

Budgets are exact US-dollar amounts stored as integer nanodollars.

- `0` means no configured ceiling, not zero permission to spend.
- A request counts settled ledger spend and every pending reservation.
- A child is checked against its own budget and every budgeted ancestor.
- Descendant spend and holds roll up to the ancestor, so siblings share the
  parent's remaining balance.
- Exact exhaustion is allowed; the next positive reservation is rejected.

Every lease belongs to an explicitly created run, so a request authenticated
with an `sll_` token is charged to that run. The older `slk_` principal-key
path remains available for compatibility. A request using a principal key and
no `X-Spendlease-Run` header is charged to an implicit run whose budget is
configured at startup:

```bash
spendlease serve --default-run-budget 25.00
```

For a principal-key request, `X-Spendlease-Run: run_...` may select an explicit
run owned by that principal. A lease token cannot be moved to another run by
setting this header; the run attached to the lease remains authoritative.

## Lease restrictions

A lease can restrict providers, lifetime, and spend:

```bash
spendlease keys lease issue \
  --run run_... \
  --ttl 15m \
  --providers openai,anthropic \
  --ceiling 5.00
```

- `--providers` is a comma-separated allowlist. A request routed to another
  provider returns `403 lease_scope_denied`.
- `--ttl` sets the expiry time. Expired and revoked leases return
  `401 unauthenticated`.
- `--ceiling` limits cumulative spend through that lease. The default `0`
  inherits the run's budget rather than creating an additional limit.

The lease ceiling and run hierarchy are checked in the same atomic reservation
transaction. Passing a smaller lease ceiling cannot increase the run budget.

Revoke every current lease for one principal with:

```bash
spendlease keys revoke --all --principal checkout-agent
```

The dashboard's Revoke button calls the same operation through the admin API.
Revocation is durable across restarts and is also placed in the gateway's
in-memory deny set for immediate checks.

## Reservation defaults

These are gateway lifecycle settings rather than per-principal policy:

| Flag | Default | Meaning |
|---|---:|---|
| `--reservation-ttl` | `15m` | Maximum lifetime of an in-flight hold before it may be reclaimed. |
| `--reservation-sweep-interval` | `30s` | How often expired pending holds are reclaimed. |
| `--enforcement-policy` | `strict` | Whether enforce-mode principals require trustworthy request bounds or accept fallback estimates. |

In observe and best-effort enforcement, the price book's model-level
`default_max_tokens` is used when a request omits its output ceiling. Unknown
models use the built-in fallback rates and fallback ceiling. Strict
enforcement rejects both cases before egress.

## Not implemented

There is no per-endpoint policy, time-of-day policy, approval workflow,
multi-currency rule, principal-level role system, anomaly detector or general
expression language. Provider scope and per-lease ceilings are enforced on
every lease; they do not expand the principal policy into a generic capability
system. The separate viewer/operator/admin roles apply only to human control-
plane access.
