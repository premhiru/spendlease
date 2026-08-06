# 26. Strict enforcement requires request bounds

## Status

Accepted.

## Context

A price-book fallback prevents an unknown model from being counted as free,
and `default_max_tokens` gives an ordinary request a practical reservation when
the caller supplies no output limit. Neither value is guaranteed to exceed a
future model's real price or maximum output. Calling that reservation a bound
for the modeled token rates would be misleading.

## Decision

`strict` is the default enforcement policy. For an output-producing request it
requires both a model in the active price book and an explicit output-token
limit. A request missing either returns `422 spend_not_enforceable` before the
vendor is contacted. Input-only routes such as embeddings do not need an output
limit.

Observe mode keeps using documented estimates so operators can discover and
price new workloads. An operator may also start the server with
`--enforcement-policy=best-effort` to retain budget blocking while accepting
fallback model rates and price-book output defaults. The dashboard names the
active policy.

Principal mode remains stored as `observe` or `enforce`. Keeping that schema
stable avoids a database rewrite and separates an agent's blocking choice from
the server operator's definition of an acceptable bound.

## Consequences

- Existing enforce-mode applications that omit an output limit receive a 422
  after upgrading until they add one or the operator chooses best-effort.
- Newly released models fail closed until their prices are reviewed and added.
- Best-effort remains useful for availability-sensitive evaluation, but it is
  explicitly identified as an estimate rather than a strict token-cost bound.
- This decision does not make unmodeled service tiers or non-token fees safe;
  those remain documented limits of the price book.
