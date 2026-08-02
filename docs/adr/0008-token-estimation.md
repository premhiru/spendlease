# 8. Bound reservations independently from usage estimates

- **Status:** Accepted
- **Date:** 2026-07-29
- **Updated:** 2026-07-31

## Context

A reservation has to be made before a request runs, which means bounding input
tokens locally. The vendor reports exact counts on completion, but an
enforcement decision cannot rely on a heuristic that may under-count.

A real BPE tokenizer would require a vocabulary per model family and would
still lag tokenizer changes. A prose heuristic such as one token per four
characters is useful for estimating actual usage, but code, JSON, punctuation,
and new model families can exceed it.

## Decision

Use two deliberately different estimates:

1. **Authorization:** reserve one input token per inspected request byte, plus
   a fixed allowance for provider-added framing and special tokens. Byte-level
   tokenizers cannot encode request content into more content tokens than
   bytes. This ceiling is intentionally conservative and is replaced at
   settlement.
2. **Fallback settlement:** when the provider reports no usage, retain the
   documented `chars/4` estimate, weighted upward for dense scripts, and mark
   the ledger entry estimated. This is a best estimate of actual usage, not an
   authorization boundary.

Requests that cannot be inspected, or contain known non-token billing
dimensions, are rejected in enforce mode rather than assigned a guess.

## Consequences

- Reservations intentionally over-hold input spend. A request can be rejected
  even though exact tokenization would have fit; the safe direction for an
  authorization system is to require a larger budget or a smaller request.
- Reported provider usage remains the settlement source of truth and releases
  the unused hold.
- Fallback ledger estimates remain approximate. English prose is usually near
  chars/4; code, JSON and punctuation can differ, and the entry says so.
- No vocabulary files, per-model tokenizer maintenance, or tokenizer-driven
  binary growth are required.

## Options rejected

- **Use chars/4 for authorization.** It can under-reserve code, JSON and model
  families with different tokenizers.
- **Vendor a BPE tokenizer.** More accurate for covered models, wrong for
  uncovered or changed models, and a permanent maintenance burden.
- **Ask the vendor to count tokens first.** It adds latency and an extra
  dependency to every authorization decision and is not uniformly available.
- **Reserve byte/4.** It under-counts non-ASCII input. The adopted reservation
  uses the full byte count rather than dividing it.
- **Reserve output only.** Long-context requests can spend primarily on input.
