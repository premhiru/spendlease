# Architecture decision records

Why things are the way they are. See [ADR-0001](0001-record-architecture-decisions.md) for the process and format.

ADRs are immutable once merged. To change a decision, add a new ADR that supersedes the old one rather than editing it.

Each record describes the code and roadmap at the time of that decision. A
statement such as "enforcement has not landed" may therefore be historically
correct even when the current implementation has moved on. Use the main guides
and references for current behavior.

| # | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-project-name-and-module-path.md) | Project name and Go module path | Accepted |
| [0003](0003-money-as-int64-nanodollars.md) | Money as int64 nanodollars | Accepted |
| [0004](0004-go-version-floor.md) | Go version floor is 1.25 | Accepted |
| [0005](0005-ledger-integrity.md) | How ledger immutability is actually enforced | Accepted |
| [0006](0006-provider-routing.md) | Routing requests to providers by path | Accepted |
| [0007](0007-credential-vault-and-master-key.md) | The credential vault, and where the master key comes from | Accepted |
| [0008](0008-token-estimation.md) | Estimating tokens without a tokenizer | Accepted |
| [0009](0009-dated-price-supersession.md) | Prices are superseded by date, never edited | Accepted |
| [0010](0010-accounting-observes-the-response.md) | Accounting observes the response, it does not buffer it | Accepted |
| [0011](0011-implicit-runs.md) | Every principal has an implicit run | Accepted |
| [0012](0012-stream-options-injection.md) | Injecting stream_options, and withholding the chunk it produces | Accepted |
| [0013](0013-the-dashboard-is-one-table.md) | The dashboard is one table | Accepted; filter decision superseded by 0017 |
| [0014](0014-budget-decisions-are-atomic-and-hierarchical.md) | Budget decisions are atomic and hierarchical | Accepted |
| [0015](0015-reservations-may-precede-leases.md) | Reservations may precede leases | Accepted |
| [0016](0016-revocation-is-memory-first-and-durable.md) | Revocation is memory-first and durable | Accepted |
| [0017](0017-filter-the-operational-event-stream.md) | Filter the operational event stream | Accepted |
