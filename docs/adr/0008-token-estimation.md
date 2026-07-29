# 8. Estimating tokens without a tokenizer

- **Status:** Accepted
- **Date:** 2026-07-29

## Context

A reservation has to be made before a request runs, which means estimating input tokens locally. The vendor reports exact counts on completion, so the estimate only has to be good enough to size a hold that is settled minutes later.

The accurate option is a real BPE tokenizer. That means vendoring a vocabulary per model family, keeping those in step with model releases, and paying the binary size and load cost for every one. It also does not fully solve the problem: vendors change tokenizers between generations. Anthropic's own documentation notes that Claude 4.7 and later "use a newer tokenizer... approximately 30% more tokens for the same text", so even a correct tokenizer for one model is wrong for its successor.

## Decision

Estimate with a documented `chars/4` heuristic, and flag every estimate as approximate.

Both major vendors publish essentially this rule of thumb: one token is roughly four characters of English. The estimate is deliberately biased upward — it rounds up, and never returns zero for non-empty input.

**Dense scripts are weighted separately.** The chars/4 rule is derived from English; Chinese, Japanese, Korean and Thai run closer to one token per character. Applying chars/4 to them would under-count by roughly four times, and under-counting is the dangerous direction: it lets through a request that should have been refused. Characters in those scripts are counted towards a near 1:1 ratio instead.

`Estimate` carries `Approximate` and `Method` fields rather than returning a bare integer, so a caller cannot use the number without seeing that it is a guess. Ledger entries built from an approximate estimate are marked estimated.

## Consequences

- No vocabulary files, no per-model tokenizer maintenance, no binary bloat. The whole estimator is a few dozen lines.
- Estimates are wrong, and the system is built to expect that: the reservation is an upper bound, and settle corrects it against reported usage. Only requests that never complete keep an estimated figure, and those are marked.
- English prose estimates within roughly ±20%. Code, JSON and heavily punctuated text tokenize less efficiently and will be under-estimated; that error is bounded by the max-tokens ceiling on the output side, which dominates most reservations.
- A tokenizer can be added later behind the same `Estimator` interface without changing any caller. If it is, `Method` should change and `Approximate` should become false for the model families it covers.

## Options rejected

- **Vendor a BPE tokenizer (tiktoken or equivalent).** More accurate for the models it covers, wrong for the ones it does not, and a permanent maintenance burden that grows with every model release. Not worth it for a number that is corrected on settle.
- **Ask the vendor to count tokens first.** Some offer a counting endpoint. It doubles the request count, adds latency to the authorization path, and costs money to find out what something will cost.
- **Estimate from byte length rather than characters.** Simpler, and badly wrong for any non-ASCII text — a UTF-8 Chinese character is three bytes, so byte/4 would under-count by roughly twelve times.
- **Skip input estimation and reserve only the output ceiling.** Tempting, since output usually dominates. Rejected because long-context requests invert that: a 500k-token prompt with a 1k completion is almost entirely input cost, and that is exactly the shape a runaway document-processing loop takes.
