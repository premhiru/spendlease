# spendlease

A spend authorization gateway for AI agents. It holds your vendor API keys, issues short-lived scoped leases to agents, enforces hard spending caps before a request is allowed out, and records an immutable ledger of every dollar each agent spent.

!!! warning "Pre-v0.1 and under construction"

    The gateway does not work yet. `serve` currently starts, logs its configuration and exits without listening. What exists today is the project scaffold, the CLI command surface, CI, and the container build.

    These documentation pages are placeholders that name the phase which fills each one. The [README](https://github.com/premhiru/spendlease#readme) describes v0.1 as designed and is the authoritative specification until the code catches up with it.

## The problem

Agents spend real money on inference, search APIs, data APIs and scrapers, and almost nobody can answer "which agent spent this?" until the invoice arrives. A retry loop with no ceiling will burn $40,000 overnight, and the first signal is a billing alert three days later.

`spendlease` puts an authorization decision in front of every one of those calls, so the loop dies at $50 instead. The mental model is **IAM for machine spend** — closer to `sts:AssumeRole` than to Expensify.

## Where to start

| | |
|---|---|
| [Getting started](getting-started.md) | Install, first principal, first lease |
| [Concepts](concepts.md) | Principal, run, lease, reservation |
| [Reserve and settle](reserve-and-settle.md) | How spend is authorized before a call completes |
| [Price book](pricing-book.md) | Cost data, and how to contribute updates |
| [Decision records](adr/README.md) | Why things are the way they are |

## Contributing

Price book updates are the lowest-barrier, highest-value contribution available: plain YAML, no Go required. See [CONTRIBUTING](https://github.com/premhiru/spendlease/blob/main/CONTRIBUTING.md#price-book-updates).
