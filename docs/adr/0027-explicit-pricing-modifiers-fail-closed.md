# 27. Explicit pricing modifiers fail closed

## Status

Accepted.

## Context

Several providers let a request select priority, flex, fast, or regional
processing. Those options can multiply or discount the ordinary token rate.
The price book describes model and token-unit prices, but it does not carry a
second set of rates for every processing option. Charging such a request at
the standard rate would make the enforced budget look more reliable than it
is.

Provider defaults and negotiated contracts are different. They live in an
external account or project and are not visible in the request body, so the
gateway cannot prove their rate from request inspection alone.

## Decision

Provider adapters inspect explicit request-level pricing modifiers. Enforce
mode accepts omission, null, and only values reviewed as ordinary-rate
processing. A premium, discounted, automatic, unknown, or malformed explicit
value returns `422 spend_not_enforceable` before the provider is contacted.

Observe mode forwards the same request with
`X-Spendlease-Accounting: unmetered` and writes no token ledger entry. The
server's best-effort policy does not relax this guard because a model fallback
cannot represent a separate processing rate.

The first guarded fields are OpenAI-compatible `service_tier` and Anthropic
`service_tier`, `speed`, and `inference_geo`. Each provider adapter owns its
ordinary-value allowlist so a shared wire format does not imply identical
billing semantics.

## Consequences

- An enforce-mode workload that explicitly requests priority, flex, fast, or
  US-only Anthropic inference fails before egress instead of being underpriced.
- New modifier values fail closed until their billing behavior is reviewed.
- Observe mode remains the evaluation path for unsupported tiers.
- Operators must still verify account defaults and negotiated prices against
  vendor billing because those settings are not present in the request.
