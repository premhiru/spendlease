# spendlease

A self-hosted reverse proxy for OpenAI and Anthropic. It keeps vendor keys in
an encrypted local vault, gives agents short-lived leases, checks run budgets
before forwarding requests, and records calculated token cost in an
append-only ledger.

!!! note "Pre-v1"

    The current `main` branch contains the planned v0.1 feature set, but there
    is no stable release. The `v0.1.0-alpha.1` tag predates the current
    implementation. Use an immutable `sha-...` container tag or pinned commit
    for repeatable evaluation.

## The problem

An agent can make billable calls without a person present. Vendor-level limits
do not explain which agent, task, or retry loop used the account. `spendlease`
adds that identity and budget decision at the request boundary.

The model is similar to short-lived infrastructure credentials: a lease is
tied to a principal and run, limited to named providers, and optionally given
a ceiling below the run budget.

## Where to start

| | |
|---|---|
| [Getting started](getting-started.md) | Install, first principal, first lease |
| [CLI reference](cli-reference.md) | Commands, flags, and environment variables |
| [SDKs and examples](sdks.md) | Python and TypeScript helpers |
| [Dashboard](dashboard.md) | Spend table, controls, and access |
| [Concepts](concepts.md) | Principal, run, lease, reservation |
| [Reserve and settle](reserve-and-settle.md) | How spend is authorized before a call completes |
| [Price book](pricing-book.md) | Cost data, and how to contribute updates |
| [Decision records](adr/README.md) | Why things are the way they are |

## Contributing

Price book updates are plain YAML and do not require Go changes. See
[CONTRIBUTING](https://github.com/premhiru/spendlease/blob/main/CONTRIBUTING.md#price-book-updates).
