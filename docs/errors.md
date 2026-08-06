# Errors

Spendlease errors are designed to be acted on. The HTTP status tells you the
class of failure; `error.type` is the stable value to use in code; and
`resolution` explains the next step for a person.

```json
{
  "error": {
    "type": "budget_exceeded",
    "message": "run run_example has $0.01 remaining, but this request needs a $0.02 reservation.",
    "resolution": "Increase the run budget, reduce max_tokens, or switch the principal to observe mode while validating the estimate.",
    "run": "run_example",
    "remaining": "0.01",
    "requested": "0.02",
    "shortfall": "0.01",
    "docs": "https://premhiru.github.io/spendlease/reserve-and-settle/"
  }
}
```

Responses also set `X-Spendlease-Error` to the same type. Messages may improve
between releases; branch on `type`, not `message`.

## Request errors

| Type | Status | What happened | What to do |
|---|---:|---|---|
| `unauthenticated` | 401 | No spendlease credential was presented, or the key is invalid, expired, or revoked. | Use the shown-once `sll_...` lease as the SDK API key. Issue a new lease if it expired or was revoked. |
| `budget_exceeded` | 402 | The worst-case reservation does not fit the run, an ancestor run, or the lease ceiling. | Lower the request's output limit, increase the correct budget, or inspect `blocking_run_id` in the response. |
| `lease_scope_denied` | 403 | The lease does not allow the provider selected by the URL. | Issue a lease whose `--providers` list includes that provider. |
| `unknown_route` | 404 | No provider adapter owns the path. | Use the provider's documented spendlease base URL, including `/kimi`, `/deepseek`, `/xai`, `/gemini`, or `/zai` where required. |
| `unknown_run` | 400 | A principal-key request selected a run that is missing or belongs to another principal. | Use a run owned by the authenticated principal, or omit `X-Spendlease-Run` to use its implicit run. |
| `spend_not_enforceable` | 422 | Strict mode could not bound the potential charge. | Supply an explicit output-token limit, use a priced model, and remove unsupported billing modifiers or features. |
| `provider_credential_missing` | 503 | The routed provider has no readable vendor key. | Store the key with `spendlease keys provider set <provider>` using the same datastore and master key as the server. |
| `upstream_unavailable` | 502 | The vendor could not be reached or the upstream connection failed. | Check vendor status, DNS, egress, proxy settings, and gateway logs. Retry only when the operation is safe to repeat. |
| `internal` | 500 | Spendlease could not complete its own operation. | Record the request ID, inspect server logs and readiness, and do not assume the provider was never contacted. |

Spendlease deliberately does not say whether a particular unknown credential
ever existed. That prevents credential enumeration.

## Diagnose a failed request

1. Read `error.type` and `resolution` from the response body.
2. Record the HTTP status, `X-Request-ID` if present, provider path, model,
   run ID, and lease ID. Never record the lease token or vendor key.
3. Check `/readyz`. A live process can still be unable to reach its datastore.
4. Filter **Recent events** by agent, result, time, run ID, or lease ID.
5. Check the structured gateway log for the same request and timestamp.
6. If spend was involved, verify the ledger before retrying an ambiguous
   operation.

## Common setup mistakes

### Browser shows `No spendlease credential was presented`

Opening a provider route such as `/kimi/v1` in a browser sends no lease and is
expected to return `401`. Provider routes are API endpoints, not pages. Open
the dashboard at `/`, and configure your SDK with the provider base URL plus
the `sll_...` lease.

### `store: not found` while creating a lease

The run ID does not exist in the datastore used by that command. Run every
`keys` and `serve` command with the same working directory, `--store`, or
`SPENDLEASE_STORE` value. Then list or recreate the run and issue the lease
again.

### The dashboard is `403` outside localhost

Remote control-plane access fails closed until a named operator exists:

```bash
spendlease keys operator create --name alice --role admin
```

Use the operator name and shown-once `slo_...` token in the browser's Basic
authentication prompt. Put TLS in front of any remote deployment.

### The request is allowed but no ledger charge appears

Inspect `X-Spendlease-Accounting`. Observe mode forwards unsupported billing
shapes as `unmetered` rather than writing a misleading token charge. Either
use a supported request shape or treat that traffic outside the spendlease
budget.

## Operator API errors

The dashboard and `/api/v1` control plane also use ordinary HTTP statuses:

- `401` means a remote operator token is missing or invalid.
- `403` means the operator role is too narrow, or a mutation is missing the
  admin/CSRF guard.
- `404` means the requested run, lease, principal, or route does not exist.
- `409 ledger_invalid` means hash-chain verification found a mismatch.
- `503` means the datastore or operator-authentication path is unavailable.

See [API reference](api-reference.md) for the complete endpoint contract and
[Self-hosting](self-hosting.md#health-metrics-and-alerts) for health, metrics,
and alerts.
