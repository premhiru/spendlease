# Price book

The price book turns reported token counts into US dollars. It lives in
[`/pricing`](https://github.com/premhiru/spendlease/tree/main/pricing) as dated
YAML files and contains selected models from all seven supported providers.

The shipped rates were checked on 2026-07-31 against the official pricing
pages for [OpenAI](https://developers.openai.com/api/docs/pricing),
[Anthropic](https://platform.claude.com/docs/en/about-claude/pricing),
[Kimi](https://platform.kimi.ai/),
[DeepSeek](https://api-docs.deepseek.com/quick_start/pricing),
[xAI](https://docs.x.ai/developers/pricing),
[Gemini](https://ai.google.dev/gemini-api/docs/pricing), and
[Z.AI](https://docs.z.ai/guides/overview/pricing). Prices and model names can
change without a spendlease release, so the date matters.

At startup the gateway computes a SHA-256 revision from every active price
file and shows its short form plus the newest effective date in the dashboard.
Future-dated files do not change the active revision until their date arrives.
Use that revision when comparing two deployments; a raw model count cannot
identify which rates either one loaded.

> [!WARNING]
> A spendlease budget covers only charges represented by the price book.
> Enforce mode blocks known unsupported billing surfaces before egress, but
> vendor pricing can still change and negotiated or regional rates can differ.
> Use observe mode and compare the ledger with the vendor console before
> enabling enforcement.

## Format

```yaml
version: 2
effective: 2026-07-31

providers:
  deepseek:
    source: https://api-docs.deepseek.com/quick_start/pricing
    models:
      deepseek-v4-flash:
        input_per_1m: 0.14
        cached_input_per_1m: 0.0028
        output_per_1m: 0.28
        default_max_tokens: 8192
```

| Field | Meaning |
|---|---|
| `version` | Schema version. The current schema is `2`; version `1` files remain readable for price history. |
| `effective` | Date on which these prices take effect. Required. |
| `source` | Vendor pricing page used to verify the rates. Required. |
| `input_per_1m` | Ordinary uncached input price per million tokens. |
| `cached_input_per_1m` | Optional cache-hit price. It falls back to the ordinary input rate when omitted. |
| `cache_write_5m_per_1m` | Optional five-minute or undifferentiated cache-write price. |
| `cache_write_1h_per_1m` | Optional one-hour cache-write price. |
| `output_per_1m` | Output price per million tokens. |
| `long_context_threshold` | Optional total-input threshold that selects the long-context rates. |
| `long_input_per_1m` | Ordinary input rate after the long-context threshold. |
| `long_cached_input_per_1m` | Cached-input rate after the threshold. |
| `long_cache_write_per_1m` | Cache-write rate after the threshold. |
| `long_output_per_1m` | Output rate after the threshold. |
| `free` | Must be `true` when a model is intentionally zero-priced; omitted or accidental zero rates are rejected. |
| `default_max_tokens` | Output ceiling reserved when the request supplies none. Must be positive. |
| `aliases` | Optional alternate model identifiers that resolve to this entry. |
| `note` | Optional context such as a deprecation or introductory rate. |

### `default_max_tokens` is a reservation default

It is not the model's output limit. It is the amount held against a run's
budget when a request does not say how much output it wants. Reserving a full
model output window would make ordinary concurrent requests needlessly block;
reserving nothing would allow an unbounded completion through.

## Superseding a price

Each file carries an `effective` date. Add a dated file when a rate changes,
even if the effective date has already passed. Editing an older rate would
erase the price history needed to interpret earlier ledger entries.

The shipped book demonstrates this with Claude Sonnet 5. Its introductory
rate applies through 2026-08-31, while a second file becomes active on
2026-09-01. The later file supersedes only that model and leaves every other
entry alone.

## Unknown models

A model the book does not contain is not treated as free. Instead:

1. The gateway logs one warning for the unknown model.
2. It applies a fallback of $15 per million input tokens, $75 per million
   output tokens, and a 4,096-token output reservation.
3. It marks the ledger entry `estimated: true`.

The fallback prevents silent zero-cost accounting, but it is not guaranteed to
exceed the real price. Add the model before relying on enforcement.

## Cost calculation

At settlement, spendlease separates the usage categories reported by the
provider and applies the corresponding per-million-token rates:

```text
cost = uncached_input × input_rate
     + cached_input × cached_rate
     + five_minute_cache_write × five_minute_write_rate
     + one_hour_cache_write × one_hour_write_rate
     + output × output_rate
```

When total input reaches a model's `long_context_threshold`, the long-context
rates apply to that request. Missing cache fields fall back to the ordinary
input rate, which keeps older version 1 files and providers without separate
cache reporting safe.

Costs use integer nanodollars throughout. Decimal prices are parsed directly,
never through floating point, and token multiplication rounds half-up. See
[ADR-0003](adr/0003-money-as-int64-nanodollars.md).

## Token estimation

Settlement uses token counts reported by the provider. A reservation must be
made before the request runs, so enforce mode uses the inspected JSON byte
count plus a provider-framing allowance as a tokenizer-independent ceiling.
This intentionally holds more than a typical tokenizer count and is replaced
by reported usage when the request finishes. When settlement has no usage,
the marked estimate still uses the documented `chars/4` heuristic. See
[ADR-0008](adr/0008-token-estimation.md).

## What is not modeled

- Batch, flex, fast, priority, and regional processing rates.
- Persistent cache-storage charges.
- Tool, search, container, and grounding charges billed per call.
- Image, audio, video, and other media-specific rates.
- Non-token services such as search APIs, scrapers, and data APIs.

Known unsupported endpoints and request features are rejected in enforce mode
with `422 spend_not_enforceable`. Observe mode forwards them, marks the
response `X-Spendlease-Accounting: unmetered`, and does not create a misleading
token ledger entry.

## Contributing an update

1. Add a dated YAML file containing only the models whose rates changed. Do
   not rewrite an older effective rate.
2. Use the date the price takes effect, not the date of the pull request.
3. Link the official vendor pricing page in the pull-request description.
4. Run `make test`. The loader validates the schema, source, model fields, and
   units before a book can be used.

See [CONTRIBUTING](https://github.com/premhiru/spendlease/blob/main/CONTRIBUTING.md#price-book-updates).
