# API reference

The HTTP surface as it exists today.

> [!NOTE]
> Pre-v1. This page documents the implemented v0.1 surface.

## Authentication

Send a short-lived lease token as either header. Both are accepted so that a
single base-URL override works for SDKs that use different conventions.

```
Authorization: Bearer sll_...
x-api-key: sll_...
```

A bare `Authorization: sll_...` with no scheme is also accepted, for hand-rolled clients.

Your credential is **always stripped** before the request is forwarded. Any vendor key you send is discarded rather than honored: the gateway decides which credential goes upstream.

Lease tokens (`sll_`) are the agent credential. They resolve to a run and
principal and enforce expiry, provider scope and the optional lease ceiling.
Principal keys remain accepted as a compatibility path, but should not be
placed in agent environments.

### Attributing spend to a run

Spend is charged to a run. Without a header, a request is charged to the principal's implicit run, created on first use — see [ADR-0011](adr/0011-implicit-runs.md).

An application that already models executions can attribute explicitly:

```
X-Spendlease-Run: run_...
```

Naming a run that does not exist, or one belonging to a different principal, returns `400 unknown_run` before the request is forwarded.

## Budget authorization

Every proxy request creates a bounded reservation before it reaches the
vendor. The gateway estimates input tokens, uses the request's output ceiling
or the price book's `default_max_tokens`, and atomically checks settled spend
plus pending holds against the run and every budgeted ancestor.

Observe-mode principals always pass; a would-block decision is logged. An
enforce-mode request that does not fit returns `402 budget_exceeded` and the
vendor is not contacted. See [reserve and settle](reserve-and-settle.md) for
the formula, concurrency guarantee and lifecycle.

## Accounting

Every successful request produces a ledger entry containing the provider,
model, reported or estimated token counts, calculated token cost, run,
and principal. Entries are append-only and hash-chained.

The cost is exact for the price-book rate and token counts used in the
calculation. Reported cache usage and documented long-context tiers are
included. It may not equal the complete vendor charge because batch, speed,
regional, cache-storage, tool, media, and other non-token charges are not
modeled. The `estimated` field identifies missing token usage or an unknown
model; it does not flag every unmodeled billing dimension. See
[Price book](pricing-book.md#what-is-not-modeled).

**Failed requests are not charged.** A non-2xx from the vendor produces no entry, because vendors do not bill for failures.

Token counts come from the vendor where it reports them. An entry is marked
`estimated` when the counts or model price require a fallback:

| Situation | Token-cost basis |
|---|---|
| Non-streaming, supported provider | Reported usage |
| Streaming, Anthropic | Reported usage |
| Streaming, OpenAI-compatible provider | Reported usage; `stream_options` is injected if needed |
| Vendor reports no usage even when asked | Estimated |
| Model not in the price book | Estimated, at the fallback rate |
| Client disconnected mid-response | Estimated, from partial usage |

## Proxy endpoints

Everything not listed under [operational endpoints](#operational-endpoints) is proxied. Routing is by path prefix — see [ADR-0006](adr/0006-provider-routing.md).

| Path | Provider |
|---|---|
| `/v1/chat/completions` | OpenAI |
| `/v1/completions` | OpenAI |
| `/v1/responses` | OpenAI |
| `/v1/embeddings` | OpenAI |
| `/v1/moderations` | OpenAI |
| `/v1/images/...` | OpenAI |
| `/v1/audio/...` | OpenAI |
| `/v1/messages` | Anthropic |
| `/v1/messages/batches` | Anthropic |
| `/v1/complete` | Anthropic |
| `/v1/models` | **Ambiguous** — see below |
| `/<provider>/...` | Named provider; the prefix is removed before forwarding |

### Ambiguous and unknown paths

`/v1/models` is claimed by OpenAI and Anthropic. The `anthropic-version`
header selects Anthropic; otherwise OpenAI wins.

To force a provider, or to reach an endpoint no adapter knows about yet, use an explicit prefix. It is stripped before forwarding:

```
POST /openai/v1/some/new/endpoint   ->  https://api.openai.com/v1/some/new/endpoint
POST /anthropic/v1/models           ->  https://api.anthropic.com/v1/models
POST /xai/v1/chat/completions       ->  https://api.x.ai/v1/chat/completions
POST /gemini/v1beta/openai/chat/completions
                                     ->  https://generativelanguage.googleapis.com/v1beta/openai/chat/completions
```

The registered provider names are `openai`, `anthropic`, `kimi`, `deepseek`,
`xai`, `gemini`, and `zai`. See [Providers](providers.md) for application base
URLs and credential setup.

### Streaming

Server-sent event responses are proxied through **unbuffered**. Each chunk is flushed to the client as it arrives, so first-token latency matches calling the vendor directly.

> [!IMPORTANT]
> **Streaming requests to OpenAI-compatible endpoints are modified.** If you did not set `stream_options: {include_usage: true}`, spendlease sets it, because otherwise the vendor reports no token counts and the call cannot be priced exactly.
>
> The extra usage chunk this produces is **withheld** from the stream you read, so what you receive is byte-identical to what you would have received without spendlease in the path.
>
> The modification announces itself on the response:
>
> ```
> X-Spendlease-Stream-Options: injected
> ```
>
> Nothing else in your request is touched, and requests you already set `stream_options` on are forwarded unchanged. Anthropic requests are never modified — the Messages API reports usage without being asked. See [ADR-0012](adr/0012-stream-options-injection.md).

### Vendor responses

Vendor responses are passed through unchanged, including error status codes and bodies. A `400` from OpenAI reaches you as OpenAI wrote it. Responses generated by spendlease itself always carry an `X-Spendlease-Error` header, so you can always tell which layer failed.

## Operational endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/healthz` | none | Liveness. Returns `{"status":"ok"}`. |
| `GET` | `/` | local or admin token | Embedded spend dashboard. |
| `GET` | `/table` | local or admin token | Dashboard table fragment used by htmx. |
| `POST` | `/admin/principals/{id}/mode` | local or admin token | Switch between `observe` and `enforce`. |
| `POST` | `/admin/principals/{id}/revoke` | local or admin token | Immediately revoke every current lease. |

“Local” means the TCP peer is a loopback address. Remote dashboard and admin
requests require `SPENDLEASE_ADMIN_TOKEN` (or `--admin-token`) and either HTTP
Basic authentication with the token as password or
`Authorization: Bearer <token>`. Without a configured token, remote access is
refused with `403`.

## Errors

Every spendlease-generated error is JSON with the same shape, and carries `X-Spendlease-Error` set to the `type`.

```json
{
  "error": {
    "type": "provider_credential_missing",
    "message": "spendlease has no openai API key, so it cannot make this request on your behalf.",
    "resolution": "Store one with: spendlease keys provider set openai --key <your openai api key>",
    "provider": "openai",
    "principal": "prn_qdvzedzj7ys6rg6repw65twoha",
    "docs": "https://premhiru.github.io/spendlease/getting-started/"
  }
}
```

`type` is stable and safe to branch on. `message` is for a human and may change.

| Type | Status | Meaning |
|---|---|---|
| `unauthenticated` | 401 | No credential, a malformed one, or one that is not recognized |
| `unknown_route` | 404 | No provider claims this path |
| `unknown_run` | 400 | The requested run is missing or belongs to another principal |
| `budget_exceeded` | 402 | The reservation exceeds the run or an ancestor's remaining budget |
| `lease_scope_denied` | 403 | The lease does not allow the resolved provider |
| `provider_credential_missing` | 503 | No vendor key stored, or it could not be decrypted |
| `upstream_unavailable` | 502 | The vendor could not be reached |
| `internal` | 500 | The gateway itself failed |

A 401 does not distinguish between a wrong key and an unknown one, deliberately: saying which would let a caller enumerate valid keys.

## Logging

One structured line per request:

```
level=INFO msg=request method=POST path=/v1/chat/completions status=200
  duration_ms=412 bytes=1204 principal=prn_qdvz... provider=openai streamed=true flushes=37
```

Request bodies, response bodies, headers and key material are never logged at any level. Rejected requests log at `WARN`, gateway failures at `ERROR`.
