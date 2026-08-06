# v1 readiness

Version 1 should mean that operators can rely on spendlease's documented
contracts and upgrade it safely. It should not mean that every possible AI
charge or orchestration framework is supported.

The project is useful today for self-hosted evaluation and carefully scoped
workloads. The remaining work is mostly proof, compatibility, and operational
hardening rather than adding another long list of providers.

## What v1 promises

A `v1.0.0` release should make these commitments explicit:

- The documented gateway routes, error types, operator API, CLI automation
  output, configuration, and ledger export schema follow a published
  compatibility policy.
- Upgrades preserve budgets, credentials, audit records, and ledger
  verification, with a tested rollback or recovery path for each migration.
- Supported provider request and response shapes are continuously certified;
  unsupported charge types fail visibly and are documented.
- A production deployment has tested health, metrics, backup, restore,
  secret rotation, and incident procedures.
- Security reports have a response process and supported release lines have a
  stated maintenance window.

Single-tenant operation can be a valid v1 scope. Multi-tenancy, SSO, hosted
billing connectors, charts, framework-specific adapters, and anomaly
detection are valuable future features, but none should be implied by the 1.0
label unless they are deliberately added to the contract.

## Release gates

| Gate | Evidence required before v1 | Current position |
|---|---|---|
| Compatibility | Written stability and deprecation policy; contract tests for public routes, error types, CLI JSON, configuration, and export schemas | Public surfaces are versioned in places, but there is no complete 1.0 compatibility policy yet |
| Data upgrades | Forward migration tests from every supported beta, documented backup and recovery, and a rehearsed failed-upgrade procedure | SQLite and PostgreSQL migrations exist; the full cross-version matrix and rollback drills remain |
| Provider certification | Live or vendor-fixture contract tests for each claimed route, a documented support matrix, and freshness alerts with named owners | Dated fixtures and price freshness checks exist; ongoing live validation and ownership need formalizing |
| Accounting accuracy | Reconciliation evidence for representative workloads, explicit tolerances, and documented treatment of unsupported charges | Itemized ledger and normalized reconciliation exist; independent billing-period evidence is still needed |
| Reliability | Published load envelope, multi-replica PostgreSQL tests, restart/outage/timeout drills, and a meaningful soak period | Core concurrency and integration tests exist; production load and failure evidence remain |
| Security | Updated threat model, independent review, dependency and artifact scanning, secret-rotation drill, and vulnerability-response targets | Strong key storage, RBAC, audit, provenance, and security policy exist; independent review remains |
| Operations | Tested dashboards/alerts, backup restore, ledger verification, upgrade, rollback, and incident runbooks | Most primitives exist; end-to-end operator drills and concise runbooks remain |
| User proof | At least three independent beta deployments, two representative production-like workloads, and resolved onboarding findings | The beta is usable, but external adoption evidence must be collected rather than assumed |
| Documentation | A new user can complete the demo, first real request, enforcement test, and recovery exercise without maintainer help | Task-first guides now exist; they still need usability testing with new users |

## Suggested path to 1.0

The version numbers are milestones, not calendar promises.

### v0.3: freeze the contract candidates

- Inventory every public interface and label it stable, experimental, or
  internal.
- Publish compatibility, deprecation, and support policies.
- Add golden contract tests for API errors, CLI JSON, configuration, and
  ledger exports.
- Test upgrades from both `v0.2.0-beta.1` and `v0.2.0-beta.2`.
- Recruit the first external beta operators and record onboarding failures.

### v0.4: prove production behavior

- Establish a tested load envelope for SQLite and PostgreSQL.
- Exercise concurrent replicas, database failover, slow vendors, disconnects,
  expired reservations, restart recovery, and webhook failures.
- Complete an independent security review and close high-severity findings.
- Reconcile representative OpenAI-compatible and Anthropic-native billing
  periods against vendor data.
- Rehearse backup restore, master-key rotation, upgrade, and rollback from the
  production checklist.

### v0.9 release candidate: stop adding scope

- Freeze features and accept only release-blocking fixes.
- Run a 30-day production-like soak on the exact release candidate.
- Require zero open critical or high-severity security findings and zero
  unresolved data-integrity failures.
- Verify every supported install path and all task-first documentation with
  users who did not build the project.
- Publish the final support window and migration guide.

### v1.0: release when the gates pass

Do not choose the date first and lower the gates to meet it. Release 1.0 when
all rows above have named evidence, the release candidate has completed its
soak, and maintainers are prepared to support the compatibility promise.

## What not to wait for

V1 does not need to solve all AI cost management. It can ship without:

- every model from every provider;
- organization-level multi-tenancy;
- a hosted SaaS control plane;
- charts or invoice generation;
- native integrations for every agent framework; or
- perfect coverage of fees that vendors do not expose before a request.

Those omissions must remain explicit. A narrow, reliable authorization
boundary is more valuable than a broad 1.0 whose accounting guarantees are
unclear.
