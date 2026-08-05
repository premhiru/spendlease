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
| [0004](0004-go-version-floor.md) | Go version floor is 1.25 | Accepted; patch-floor policy amended by 0023 |
| [0005](0005-ledger-integrity.md) | How ledger immutability is actually enforced | Accepted; entry format amended by 0024 |
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
| [0018](0018-version-the-operator-control-plane.md) | Version the operator control plane | Accepted |
| [0019](0019-postgresql-multi-instance-storage.md) | PostgreSQL is the multi-instance storage backend | Accepted; amends 0005 and 0014 |
| [0020](0020-external-key-sources-and-rotation.md) | External key sources and transactional rotation | Accepted; amends 0007 |
| [0021](0021-named-operators-rbac-and-audit.md) | Named operators, RBAC, and append-only audit | Accepted; amends 0018 |
| [0022](0022-operational-signals-and-bounded-networking.md) | Operational signals and bounded networking | Accepted |
| [0023](0023-release-artifacts-are-verifiable.md) | Release artifacts are independently verifiable | Accepted; amends 0004 |
| [0024](0024-itemized-usage-and-reconciliation.md) | Itemized usage and normalized reconciliation | Accepted; amends 0005 |
| [0025](0025-dashboard-onboarding-is-a-guarded-control-plane.md) | Dashboard onboarding is a guarded control plane | Accepted; amends 0013, 0018, and 0021 |
