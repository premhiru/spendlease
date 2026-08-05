# Reconcile with a vendor statement

`spendlease ledger reconcile` compares the append-only ledger with a
provider-neutral CSV statement. It does not change budgets, rewrite ledger
entries, or declare either side authoritative. The report tells an operator
where to investigate before closing a billing period.

## Normalize the vendor export

Vendor exports use different names and aggregation levels, so spendlease
accepts one small interchange format rather than guessing at each vendor's
private invoice schema:

```csv
provider,model,usage_json,cost_usd,occurred_at,external_id
openai,gpt-5,"{""input_tokens"":12500,""cached_input_tokens"":8000,""output_tokens"":700}",0.0435,2026-08-03T12:01:02Z,req_abc123
anthropic,claude-sonnet-5,"{""input_tokens"":4000,""cache_write_5m_tokens"":1200,""output_tokens"":350}",0.0312,2026-08-03T12:04:17Z,req_789
```

The required columns are:

| Column | Meaning |
|---|---|
| `provider` | spendlease provider name, such as `openai`, `anthropic`, or `kimi`. |
| `model` | Model identifier used for grouping. Normalize vendor aliases deliberately. |
| `usage_json` | JSON object whose keys are billable units and values are non-negative integers. |
| `cost_usd` | Exact non-negative decimal USD charge for the row, with at most nine decimal places. |
| `occurred_at` | RFC 3339 timestamp used to select the accounting period. |
| `external_id` | Optional upstream request ID. It improves drill-down coverage but is not required. |

The current gateway records `input_tokens`, `cached_input_tokens`,
`cache_write_5m_tokens`, `cache_write_1h_tokens`, and `output_tokens`
separately. The interchange format accepts other lowercase named integer
units, such as `web_search_calls` or `image_count`. Those dimensions are
visible in a reconciliation difference even when the gateway cannot yet
reserve or enforce them.

Keep the original vendor export with the normalized file. The conversion is
part of the audit trail, especially when a vendor reports a model alias,
applies credits, or rounds at invoice level.

## Run the comparison

The period is half-open: `--since` is included and `--until` is excluded.

```bash
spendlease ledger reconcile \
  --statement vendor-normalized.csv \
  --since 2026-08-01T00:00:00Z \
  --until 2026-09-01T00:00:00Z \
  --cost-tolerance 0.01 \
  --format json \
  --store /var/lib/spendlease/spendlease.db \
  > reconciliation.json
```

Results are grouped by provider and model. Cost delta and every usage delta
are `statement - ledger`, so a positive value means the vendor statement is
higher. The status is one of:

| Status | Meaning |
|---|---|
| `match` | Cost is within tolerance and all named usage totals agree. |
| `cost_mismatch` | Absolute cost difference exceeds the per-group tolerance. |
| `usage_mismatch` | Cost is within tolerance but one or more usage dimensions differ. |
| `ledger_only` | The period contains ledger spend with no statement group. |
| `statement_only` | The statement contains spend with no ledger group. |

Use `--format csv` for a flat review file. The usage columns remain JSON
objects so adding a new billable unit does not change the CSV header. Add
`--fail-on-mismatch` in an automated close process; the full report is written
before the command exits non-zero.

`matched_external_ids` is a coverage signal, not the matching algorithm. The
report aggregates by provider/model because some vendors omit request IDs or
publish only daily totals. A low match count tells you the normalized export
cannot support request-by-request drill-down.

## Interpret differences

Common causes are a price change not yet present in the price book, negotiated
or regional rates, invoice-level rounding, model aliases, delayed vendor
events, and billing units that enforce mode deliberately blocks. Compare the
entry's `pricing_revision` and `price_effective` fields with the active price
book before changing a rate.

Ledger hash version 2 preserves itemized usage and pricing provenance. Rows
created before that migration remain valid version 1 entries and are expanded
as aggregate `input_tokens` and `output_tokens`; their historical cached-input
split cannot be reconstructed after the fact.

This tool is not an ERP connector and does not close books. It creates a
repeatable, exact comparison that can feed the finance process you already
use.
