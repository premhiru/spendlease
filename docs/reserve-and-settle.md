# Reserve and settle

Spend authorization has to happen before a vendor call, while final token
usage is only known after it. `spendlease` treats the gap like a fuel-pump
pre-authorization: hold a safe upper bound, then replace the hold with the
calculated token charge.

Reservations use the active price book. Enforce mode refuses request shapes
with charges outside that model, including media, batches and provider-hosted
tool fees. Observe mode may forward them as explicitly unmetered traffic; see
[Price book](pricing-book.md#what-is-not-modeled).

## The reservation

For every supported token-billed request the gateway resolves its provider,
model, principal and run, then calculates:

```text
input_tokens  = inspected request bytes + provider-framing safety allowance
output_tokens = request output ceiling, or the model's configured default
reserved      = input cost + maximum output cost
```

The byte-based input ceiling is deliberately conservative. Settlement uses
vendor-reported token counts, so over-reserving does not increase the recorded
charge. Embeddings reserve input only. The gateway understands
`max_tokens`, `max_completion_tokens`, and `max_output_tokens`.

A missing `max_tokens` never means infinity. The active price-book entry
supplies `default_max_tokens`; an unknown model uses the built-in fallback
rates and fallback output ceiling. Unknown models are marked estimated in the
ledger and produce a warning, but they never cost zero.

The default reservation TTL is 15 minutes. The `--reservation-ttl` flag
changes it. A background sweep, every 30 seconds by default, expires abandoned
holds. Both values are runtime configuration rather than policy fields.

## One atomic decision

Remaining budget is:

```text
budget - settled ledger spend - pending reservations
```

Checking those values and inserting the new hold is one datastore transaction.
They are not three independent operations: two concurrent requests must not
both observe the same remaining dollar and both spend it.

A run with a zero budget has no configured ceiling. For a child run, the same
check is made against the child and every budgeted ancestor. Spend and pending
holds from every descendant count against an ancestor, so sibling agents draw
from the same parent balance. The first insufficient run in that chain is
named in the rejection.

## Observe and enforce

Both modes calculate and persist reservations for supported token-billed
requests.

- `observe` always forwards the request. A decision that would exceed a
  budget is logged as `would_block`, which lets an operator validate prices
  before putting the gateway in the blocking path.
- `enforce` inserts the reservation only when every applicable budget can
  cover it. Otherwise the vendor is never contacted and the client receives
  `402 Payment Required`.

If a request cannot be inspected or has an unsupported billing dimension,
enforce mode returns `422 spend_not_enforceable` before egress. Observe mode
forwards it without a reservation or ledger charge, logs the reason, and sets
`X-Spendlease-Accounting: unmetered` on the response.

Example:

```json
{
  "error": {
    "type": "budget_exceeded",
    "message": "run run_checkout has $0.40 remaining, but this request needs a $0.75 reservation.",
    "resolution": "Increase the run budget, reduce max_tokens, or switch the principal to observe mode while validating the estimate.",
    "principal": "prn_checkout",
    "run": "run_checkout",
    "budget": "10.00",
    "spent": "9.10",
    "held": "0.50",
    "requested": "0.75",
    "shortfall": "0.35",
    "docs": "https://premhiru.github.io/spendlease/reserve-and-settle/"
  }
}
```

Money fields are decimal US-dollar strings. The stable field to branch on is
`error.type`; prose may improve without a breaking API change.

## Settlement

On a successful vendor response, spendlease reads the vendor's usage and
atomically appends the ledger entry while resolving the reservation. The
ledger charge is the base token cost calculated from that usage, not the
reserved ceiling; the unused difference becomes available immediately.

Settlement is idempotent. A reservation can produce at most one ledger entry,
including when a process retries after an uncertain database result.

### Provider errors

A non-2xx vendor response releases the full hold and creates no ledger entry.
Vendors do not bill failed calls, so neither does spendlease. A transport error
before a response does the same.

### Client disconnects and streaming

SSE is still passed through without response buffering. Usage events are read
as chunks travel to the client. OpenAI-compatible requests may have
`stream_options: {include_usage: true}` injected as documented in the
[API reference](api-reference.md#streaming).

If the client disconnects after usage has been observed, that partial usage is
settled. If usage is unavailable, the charge uses the documented local
estimate and is marked `estimated: true`. Closing the client connection does
not erase spend already incurred upstream.

If the process disappears before it can settle or release, the reservation
remains pending until its TTL, then the sweeper reclaims it. A late response
may still append its calculated charge after expiry, but cannot reclaim the
hold a second time.

## Failure policy

Enforcement fails closed before egress: if the gateway cannot make an atomic
budget decision, an enforce-mode request is not sent to the vendor. Observe
mode remains non-blocking but logs the accounting failure loudly.

After egress, ledger or settlement errors cannot retract a response already
delivered to the caller. They are logged at error level, and the pending hold
remains until a retry or TTL expiry rather than being silently released.
