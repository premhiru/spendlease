# Price book

The price book is how `spendlease` turns token counts into dollars. It lives in [`/pricing`](https://github.com/premhiru/spendlease/tree/main/pricing) as plain YAML — data, not code.

**This is the easiest and most valuable thing to contribute to.** Updating a price needs no Go, no tests to write, and no understanding of the gateway. Vendor prices change constantly and nobody else maintains a normalised cost table across every vendor an agent might call.

## Format

```yaml
version: 1
effective: 2026-07-01

providers:
  openai:
    source: https://developers.openai.com/api/docs/pricing
    models:
      gpt-4o:
        input_per_1m: 2.50
        output_per_1m: 10.00
        default_max_tokens: 4096
```

| Field | Meaning |
|---|---|
| `version` | Schema version. Currently `1`. A file declaring anything else is refused rather than guessed at. |
| `effective` | The date these prices take effect. **Required.** |
| `source` | The vendor's public pricing page. **Required** — a price without a source cannot be reviewed. |
| `input_per_1m` | USD per one million input tokens. |
| `output_per_1m` | USD per one million output tokens. |
| `default_max_tokens` | Output ceiling assumed when a request specifies no `max_tokens`. Must be positive. |
| `aliases` | Optional. Other identifiers resolving to this entry, for vendors publishing both a dated id and a convenience alias. |
| `note` | Optional. Anything a reviewer should know, such as a deprecation. |

### `default_max_tokens` is a reservation default

It is **not** the model's output limit. It is what gets held against a run's budget when a request does not say how much it wants.

Reserving a model's full output window (128k tokens on current flagship models) would tie up most of a budget for a request that will almost certainly use a fraction of it, and would reject every subsequent call. Reserving nothing would let an unbounded completion through. A few thousand tokens is the sensible middle.

## Prices are never edited, only superseded

Each file carries an `effective` date. A price change ships as a **new dated file**, not an edit to an existing one.

The shipped book demonstrates this with a real example. Claude Sonnet 5 is on introductory pricing through 2026-08-31:

```yaml
# anthropic.yaml — effective 2026-07-01
claude-sonnet-5:
  input_per_1m: 2.00
  output_per_1m: 10.00
```

```yaml
# anthropic-2026-09-01.yaml — effective 2026-09-01
claude-sonnet-5:
  input_per_1m: 3.00
  output_per_1m: 15.00
```

Until 1 September the loader ignores the second file entirely. From that date it supersedes the earlier entry **for that model only**, leaving every other model alone.

This matters because a ledger entry written in July has to stay explainable in December, and it can only be explained against the price that was in force when it was written.

## Unknown models

A model the book does not contain **never costs zero**. Instead:

1. A warning is logged, once per unknown model, so a retry loop does not flood the log.
2. A configurable fallback rate is applied. The default is deliberately expensive — roughly the priciest model in the book.
3. The resulting ledger entry is marked `estimated: true`.

Guessing high means an unknown model over-reserves and is throttled early, which is recoverable. Guessing low means a runaway loop runs longer than the budget should have allowed, which is the failure this product exists to prevent.

## Cost calculation

Costs are exact integers throughout. Prices are parsed from decimal text directly into `int64` nanodollars, never through a float — see [ADR-0003](adr/0003-money-as-int64-nanodollars.md).

```
cost = input_tokens  × input_per_1m  ÷ 1,000,000
     + output_tokens × output_per_1m ÷ 1,000,000
```

The implementation splits the token count into whole millions and a remainder, because the naive multiplication overflows `int64` for large requests. The remainder is rounded half-up rather than truncated: truncation would bias every charge downwards, and a spend limiter that consistently under-counts is the wrong kind of wrong.

Verified against a vendor's own worked example — 50,000 input and 15,000 output tokens on Claude Opus 5 costs $0.25 + $0.375 — which is the closest thing to an independent oracle available.

## Token estimation

Costs at settle time use the token counts the vendor reports. But a reservation has to be made *before* the request runs, so input tokens are estimated locally.

There is **no bundled tokenizer**. The estimate uses a documented `chars/4` heuristic, weighted upward for dense scripts (Chinese, Japanese, Korean, Thai) where one character is close to one token. Estimates are always flagged approximate. See [ADR-0008](adr/0008-token-estimation.md).

The estimate only has to be good enough to size a hold that gets settled against actual usage minutes later.

## What is not modelled

Being clear about this matters more than covering everything:

- **Prompt caching.** Cache writes and reads are priced differently (1.25×, 2×, 0.1× of base input on Anthropic), but whether a given request hit cache is not reliably knowable from outside. A cache-heavy workload is therefore over-estimated, which is the safe direction.
- **Batch API discounts.** 50% on both vendors, but only for asynchronous batch endpoints.
- **Priority and fast-mode tiers**, and data-residency multipliers.
- **Per-search and per-container tool charges**, such as web search billed per thousand searches.
- **Non-token spend entirely** — search APIs, scrapers, data APIs. The ledger can hold these; the price book does not describe them yet.

## Contributing an update

1. Edit the relevant file under `/pricing`, or add a new dated one for a scheduled change.
2. Set `effective` to the date the price actually takes effect, not today.
3. **Link the vendor's pricing page in your PR description.** This is the one hard requirement.
4. Run `make test`. The price book is schema-validated and sanity-checked by the test suite — it will catch a missing source, a non-positive rate, output priced below input, and prices large enough to suggest a units error.

See [CONTRIBUTING](https://github.com/premhiru/spendlease/blob/main/CONTRIBUTING.md#price-book-updates).
