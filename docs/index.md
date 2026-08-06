# spendlease

A self-hosted reverse proxy for OpenAI, Anthropic, Kimi, DeepSeek, xAI,
Gemini, and Z.AI. It keeps vendor keys in
an encrypted local vault, gives agents short-lived leases, checks run budgets
before forwarding requests, and records calculated token cost in an
append-only ledger.

!!! note "Pre-v1"

    `v0.2.0-beta.1` is the current release and the first intended for an
    end-to-end evaluation. It is not a stable v1 API. Use the release's
    immutable container digest or a pinned commit for repeatable evaluation.
    Current `main` and `:edge` default to strict enforcement; the tagged beta
    predates that change and uses the behavior now named `best-effort`.

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
| [Providers](providers.md) | Credentials, base URLs, routing, and pricing scope |
| [CLI reference](cli-reference.md) | Commands, flags, and environment variables |
| [SDKs and examples](sdks.md) | Python and TypeScript helpers |
| [API reference](api-reference.md) | Versioned run, lease, budget, and ledger operations |
| [Dashboard](dashboard.md) | Agent setup, provider keys, spend, leases, and access controls |
| [Concepts](concepts.md) | Principal, run, lease, reservation |
| [Reserve and settle](reserve-and-settle.md) | How spend is authorized before a call completes |
| [Price book](pricing-book.md) | Cost data, and how to contribute updates |
| [Decision records](adr/README.md) | Why things are the way they are |

## Contributing

Price book updates are plain YAML and do not require Go changes. See
[CONTRIBUTING](https://github.com/premhiru/spendlease/blob/main/CONTRIBUTING.md#price-book-updates).
