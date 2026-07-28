# 6. Routing requests to providers by path

- **Status:** Accepted
- **Date:** 2026-07-28

## Context

The gateway's whole integration story is a base-URL override:

```python
client = OpenAI(base_url="http://localhost:4000/v1", api_key=SPENDLEASE_KEY)
```

That works with every vendor SDK in every language and never rots, which is why no framework-specific integrations are being built. But it means the gateway receives a request with no explicit statement of which vendor it is for. Something has to decide.

The obvious signals are all flawed:

- **A header.** Requires the caller to set it, which means editing client code, which is exactly what base-URL override avoids.
- **The model name in the body.** Requires parsing every request body, including streaming ones, before routing. It also fails for requests without a model, and ties routing to a price-book-like registry of which vendor owns which model name.
- **A separate port per vendor.** Ugly to deploy, and multiplies the container's surface.

Path is the remaining signal, and it is nearly sufficient: OpenAI clients send `/v1/chat/completions`, Anthropic clients send `/v1/messages`. Nearly, because `/v1/models` is claimed by both.

## Decision

Routing is by **path prefix**, with two tie-breakers.

1. **An explicit `/<provider>/...` prefix always wins** and is stripped before forwarding. `/openai/v1/anything` goes to OpenAI, whatever the rest of the path is. This is the escape hatch for ambiguity and for vendor routes this code does not yet know about.
2. **For a path claimed by more than one provider**, the `anthropic-version` header decides, because only Anthropic clients send it. Failing that, the first registered provider wins.

An unrecognised path is a `404` naming the known providers and showing the explicit-prefix form, rather than being silently forwarded somewhere.

## Consequences

- The one-line integration works unmodified for both vendors, which is the property the whole adoption story rests on.
- A vendor adding a new endpoint needs a one-line change to that adapter's `Paths()`, and until then the explicit prefix keeps callers unblocked. Nobody has to wait for a release.
- Adding a third provider whose paths collide with an existing one will need a new tie-breaker. The registry is the single place that changes.
- Routing never reads the request body, so it costs nothing on a streaming request and cannot be confused by a malformed one.
- A caller who points an Anthropic SDK at the gateway and asks for `/v1/models` without the version header gets OpenAI's model list. This is the one genuinely surprising outcome, and it is why the explicit prefix is documented in the error message.

## Options rejected

- **Parse the model name from the body.** Rejected above: forces buffering the body before routing, which is directly at odds with streaming, and needs a vendor-to-model registry that would drift.
- **Require an explicit prefix always.** Simple and unambiguous, but breaks the promise that an unmodified SDK works. `base_url=".../openai/v1"` is still a one-line change, but it is a spendlease-shaped one-line change, and every quickstart would carry an explanation.
- **One listener per provider.** Removes ambiguity entirely, at the cost of a port per vendor, more container configuration, and a worse story for the single `docker run` command.
- **Route on the `User-Agent` of the vendor SDK.** Fragile, undocumented by vendors, and silently wrong for anything hand-rolled.
