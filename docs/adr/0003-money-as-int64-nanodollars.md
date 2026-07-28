# 3. Money as int64 nanodollars

- **Status:** Accepted
- **Date:** 2026-07-28

## Context

`spendlease` exists to agree with a vendor invoice. Every budget, price, reservation and ledger entry is an amount of money, and the whole product is worthless if those amounts drift.

Two properties make this harder than typical currency handling:

**Amounts are extremely small.** A single `gpt-4o` input token costs $0.0000025. Cent precision is useless here, and even microdollar precision (1e-6) cannot represent that price — it would round to either 2 or 3 microdollars, a 20% per-token error that compounds across millions of tokens.

**Amounts are summed constantly.** A run's spend is the sum of every entry charged to it. Any per-operation error accumulates rather than cancelling out.

## Decision

All monetary amounts are `int64` counts of **nanodollars** (1e-9 USD), represented by `money.Nanos`. No floating point value is ever stored, summed, compared, or persisted.

`money.ParseUSD` converts decimal strings to `Nanos` by splitting on the decimal point and parsing each side as an integer — it never constructs a `float64` as an intermediate. `Nanos.String` renders back to an exact decimal string that round-trips.

A `Nanos.USD() float64` method exists, documented as display-only: for rendering in a dashboard or serialising to JSON where a human will read it. Using it for arithmetic is a bug.

`int64` nanodollars span roughly ±$9.2 billion, which is far beyond any budget this system will hold. `ParseUSD` rejects a tenth decimal place rather than rounding it, because a price that cannot be represented exactly is a data problem the operator needs to hear about.

## Consequences

- Sums are exact. Ten amounts of $0.10 total exactly $1.00, and a million `gpt-4o` input tokens total exactly $2.50. Both are tested.
- Prices from the price book, which arrive as decimal YAML, parse to exact integers.
- Every layer speaks one money type, so there is no conversion boundary at which precision could be lost.
- Amounts crossing the API as JSON numbers need care: JSON numbers are IEEE 754 doubles in most parsers, so large nanodollar integers must be sent as strings. That constraint lands in the API phase.
- Multi-currency is out of scope and this decision assumes it stays that way. Supporting it would need a currency tag alongside every amount, not a different numeric type.

## Options rejected

- **`float64` dollars.** Binary floating point cannot represent 0.1 exactly. A budget system that disagrees with an invoice about the third decimal place is worse than no budget system, and the failure is silent and cumulative.
- **`int64` cents.** Cannot represent any realistic per-token price at all. Would force rounding at the point where precision matters most.
- **`int64` microdollars.** Nearly sufficient, and tempting because it fits familiar "millionths" conventions. Rejected because $0.0000025 is 2.5 microdollars — the single most common price in the system is exactly the case it cannot represent.
- **`math/big.Rat` or a decimal library.** Exact and general, but allocates on every operation, is slower on a per-request hot path, adds a dependency, and does not serialise to a plain database integer. Nanodollars give exactness with none of that cost.
- **Storing the vendor's decimal string and parsing on demand.** Pushes the problem to every read site and makes `SUM()` in SQL impossible.
