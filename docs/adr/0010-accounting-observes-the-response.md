# 10. Accounting observes the response, it does not buffer it

- **Status:** Accepted
- **Date:** 2026-07-29

## Context

To record what a request cost, spendlease needs the token counts the vendor reports. Those arrive in the response body: at the top level for a normal reply, and spread across events for a streamed one.

The obvious implementation reads the whole response, parses it, records the cost, and then forwards it. That would destroy streaming — the property phase 3 established, tested, and verified against a real binary. An agent waiting on first-token latency would see nothing until the completion finished.

There is also a question of when accounting can fail the request. Reserving budget *must* be able to refuse a call; that is the product. Recording what already happened cannot refuse anything, because the money has already been spent.

## Decision

**The response body is wrapped, not buffered.** `observingReader` sits between the upstream body and the proxy's copy loop. Every byte is forwarded the instant it arrives, exactly as received, and the same bytes are fed to a parser on the way past.

For a streamed response it scans server-sent events as they pass, carrying a partial line across read boundaries. For a normal response it accumulates up to 1 MiB — enough for any reply that carries a usage object, bounded so that a megabyte-scale embeddings response cannot turn the proxy into a memory leak. Past the cap the body still reaches the client untouched; only the accounting falls back to an estimate.

**Recording never fails a request.** It runs when the body is exhausted or closed, after the response has been delivered. An error appending to the ledger is logged and swallowed. Refusing to serve traffic because the ledger is unavailable would make spendlease a liability rather than a safeguard.

Enforcement is the opposite and belongs elsewhere: it runs *before* the request and has to fail closed.

**A close without EOF is a client disconnect**, not an error. Whatever usage was seen is still recorded, marked estimated with the reason. Recording nothing for the requests most likely to indicate a runaway agent would be perverse.

Recording uses a context detached from the request, because a disconnect has already cancelled the original.

## Consequences

- Streaming latency is unchanged. Verified against a running binary: chunks arrive at the interval the upstream produces them, with accounting active.
- Anthropic streams price exactly, because the vendor reports usage on every stream without being asked. OpenAI streams price exactly only when the caller set `stream_options.include_usage`; otherwise the entry is estimated and says so. Injecting that option is request mutation and belongs with reserve-and-settle, not here.
- A vendor that changes its usage field names silently degrades to estimates rather than breaking. The `estimated` flag and its reason make that visible instead of invisible.
- The 1 MiB cap is a real limit. A response larger than that with usage at the very end is accounted by estimate. No current endpoint behaves that way, and the cap can rise if one appears.

## Options rejected

- **Buffer the response and account exactly.** Simplest, and it would undo the single most carefully verified property in the project.
- **Account from a second, parallel request.** Doubles vendor spend to measure vendor spend.
- **Record asynchronously on a queue.** Removes the (already negligible) latency of a local append, at the cost of losing entries on shutdown and needing a durable queue — an entire subsystem to avoid a sub-millisecond write.
- **Fail the request when the ledger write fails.** Considered seriously, because silently losing spend records is bad. Rejected because the failure mode is worse: an outage in the accounting path would take down the traffic it is meant to protect. Enforcement, which does fail closed, is the right place for that stance.
