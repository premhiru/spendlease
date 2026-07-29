# 12. Injecting stream_options, and withholding the chunk it produces

- **Status:** Accepted
- **Date:** 2026-07-29

## Context

OpenAI-compatible endpoints report token usage on a streamed response **only** when the request sets `stream_options: {include_usage: true}`. Almost no client sets it, because almost no client cares.

spendlease cares. Without those counts a streamed call can only be estimated, and streamed chat completions are the dominant shape of agent traffic. A spend ledger whose most common entry is a guess is not much of a ledger.

The fix is one field. The problem is that adding it means **modifying somebody else's request**, which is exactly the kind of thing a proxy should not do quietly. Anthropic needs none of this: the Messages API reports usage on every stream without being asked.

## Decision

**Inject it, withhold the consequence, and announce that both happened.**

1. A streaming request to an OpenAI-compatible provider that does not already ask for usage gets `stream_options.include_usage` set. Nothing else changes: the body is decoded, one field is set, and the rest is re-encoded as it was. A body that will not round-trip is forwarded untouched — losing exact accounting is a far smaller failure than corrupting a request.

2. The extra chunk that results is **withheld from the client**. Having asked for usage on the caller's behalf, delivering the answer to them would change the shape of a stream they did not ask to change. What they read is what they would have read without spendlease in the path.

3. The response carries `X-Spendlease-Stream-Options: injected`. A modification that leaves no trace is the surprising kind; one discoverable from a single `curl -i` is not.

Requests that already set the option are forwarded unchanged and their usage chunk is passed through, because that one the caller asked for. Non-streaming requests are never touched. Anthropic requests are never touched.

## Consequences

- Streamed OpenAI calls are now priced from reported usage rather than estimated. This was the last significant accuracy gap in observe mode.
- Withholding an event means the filtering path has to hold each event until it is complete before deciding, rather than forwarding bytes the instant they arrive. **This applies only to requests spendlease modified**; every other response stays a byte-for-byte pass-through, which keeps the verified streaming path untouched for the majority of traffic. There is a test asserting the filtered path still delivers chunks incrementally, and one asserting a usage chunk split across read boundaries is still withheld.
- A client that inspects raw SSE frames and counts them will see one fewer than the vendor sent. That is the intended outcome, and the header says so.
- An OpenAI-compatible gateway that ignores `stream_options` degrades to an estimate marked with its reason, rather than failing.

## Options rejected

- **Do not inject; estimate instead.** This was the original decision, on the grounds that request mutation is surprising and belongs with reserve-and-settle. Overruled: the accuracy cost falls on the most common request shape there is, and "observe mode records everything" is worth much less if the most common entry is a guess.
- **Inject and pass the usage chunk through.** Simpler, no filtering, no buffering question at all. Rejected because it changes what the caller's stream contains as a side effect of an internal need. A client parsing chunks strictly could break on a frame with an empty `choices` array.
- **Require callers to set the option themselves.** Honest, and it makes exact accounting opt-in — inverting the adoption story, since the people least likely to read the documentation are the ones most likely to run a runaway loop.
- **Count output tokens locally from the streamed content.** No request modification at all, and it means re-implementing a tokenizer per model family with all the drift that implies. See [ADR-0008](0008-token-estimation.md).
