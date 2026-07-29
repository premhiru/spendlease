# Price book

The price book turns token counts into US dollars. It lives in
[`/pricing`](https://github.com/premhiru/spendlease/tree/main/pricing) as YAML
and currently contains base token rates for selected OpenAI and Anthropic
models.

The shipped rates were checked against the
[OpenAI](https://developers.openai.com/api/docs/pricing) and
[Anthropic](https://platform.claude.com/docs/en/about-claude/pricing) pricing
pages on 2026-07-29. They still need regular review because model names and
prices can change without a spendlease release.

> [!WARNING]
> A spendlease budget currently covers the token charges represented by the
> price book. It is not a guaranteed ceiling on the complete vendor invoice.
> Cache-write multipliers, long-context rates, regional processing, tool-call
> fees, and other non-token charges are not modeled. Use observe mode and
> compare the ledger with the vendor bill before enabling enforcement.

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

## Superseding a price

Each file carries an `effective` date. Add a dated file when a rate changes,
even if the effective date has already passed. Editing an older rate would
make the repository lose the price history used to interpret earlier ledger
entries.

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

A model the book does not contain is not treated as free. Instead:

1. A warning is logged, once per unknown model, so a retry loop does not flood the log.
2. A fixed fallback of $15 per million input tokens, $75 per million output
   tokens, and a 4,096-token output reservation is applied.
3. The resulting ledger entry is marked `estimated: true`.

The fallback prevents silent zero-cost accounting, but it is not guaranteed to
be higher than the unknown model's real price. Treat the warning and
`estimated` marker as a request to add the model before relying on enforcement.

## Cost calculation

Costs are exact integers throughout. Prices are parsed from decimal text directly into `int64` nanodollars, never through a float — see [ADR-0003](adr/0003-money-as-int64-nanodollars.md).

```
cost = input_tokens  × input_per_1m  ÷ 1,000,000
     + output_tokens × output_per_1m ÷ 1,000,000
```

The implementation splits the token count into whole millions and a remainder, because the naive multiplication overflows `int64` for large requests. The remainder is rounded half-up rather than truncated: truncation would bias every charge downwards, and a spend limiter that consistently under-counts is the wrong kind of wrong.

The test suite includes worked examples and boundary cases for integer
rounding and overflow.

## Token estimation

Costs at settle time use the token counts the vendor reports. But a reservation has to be made *before* the request runs, so input tokens are estimated locally.

There is **no bundled tokenizer**. The estimate uses a documented `chars/4` heuristic, weighted upward for dense scripts (Chinese, Japanese, Korean, Thai) where one character is close to one token. Estimates are always flagged approximate. See [ADR-0008](adr/0008-token-estimation.md).

The estimate only has to be good enough to size a hold that gets settled against actual usage minutes later.

## What is not modeled

- **Prompt caching.** Cache reads can cost less than base input and cache writes
  can cost more. Pricing all input at the base rate can therefore overcount or
  undercount, depending on the workload.
- **Long-context multipliers.** Some models charge higher input and output
  rates after a context threshold.
- **Batch discounts, priority tiers, fast-mode tiers, and regional processing
  multipliers.**
- **Tool charges**, such as search or container use billed per call.
- **Non-token providers**, including search APIs, scrapers, and data APIs.

## Contributing an update

1. Add a dated YAML file containing the models whose rates changed. Do not
   rewrite an older effective rate.
2. Set `effective` to the date the price actually takes effect, not today.
3. **Link the vendor's pricing page in your PR description.** This is the one hard requirement.
4. Run `make test`. The price book is schema-validated and sanity-checked by the test suite — it will catch a missing source, a non-positive rate, output priced below input, and prices large enough to suggest a units error.

See [CONTRIBUTING](https://github.com/premhiru/spendlease/blob/main/CONTRIBUTING.md#price-book-updates).
