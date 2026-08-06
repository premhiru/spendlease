# Changelog

This file records user-visible changes. Release tags and package versions use
the same version; Python prerelease spelling follows PEP 440.

## Unreleased

### Added

- The dashboard can create an agent, its first budgeted run, and a scoped
  one-time lease in one form. Operators can create and close runs, issue new
  leases, and revoke individual leases without using the CLI.
- Admins can store, replace, and remove provider API keys from the dashboard.
  The UI reports configuration status but never reads a key back from the
  encrypted credential vault.

### Changed

- Enforce-mode principals now fail closed by default when a model is absent
  from the active price book or an output-producing request supplies no token
  ceiling. `--enforcement-policy=best-effort` explicitly restores fallback
  model prices and price-book output defaults, and the dashboard names the
  running policy.

## [0.2.0-beta.1] - 2026-08-05

The first beta intended for an end-to-end, self-hosted evaluation.

### Added

- Atomic run, parent, and principal budgets with reserve-then-settle accounting.
- Short-lived, individually revocable leases and an immediate in-process kill
  switch.
- OpenAI, Anthropic, Gemini, Kimi, DeepSeek, xAI, and Z.AI gateway routes, with
  dated pricing for 63 model identifiers.
- A versioned operator API, Python and TypeScript helpers, named operator roles,
  and an append-only operator audit trail.
- PostgreSQL support for multi-instance deployments, external master-key
  sources, and transactional master-key rotation.
- Health and readiness probes, Prometheus metrics, structured request logs,
  bounded request bodies and timeouts, alert webhooks, and graceful shutdown.
- Itemized usage in the immutable ledger, pricing provenance, upstream request
  IDs, and normalized vendor-statement reconciliation.
- Reproducible container and binary builds with checksums, SPDX SBOMs, signed
  attestations, dependency review, and vulnerability scanning.

### Changed

- New ledger rows use hash format 2. Existing hash-format-1 rows remain valid.
- JSON ledger exports use schema version 2 and include itemized usage and
  pricing provenance. CSV exports add matching columns.
- Production deployments require an explicit master-key source. Named operator
  tokens replace the shared admin token; the shared token remains available
  only as a migration path.
- The supported Go security floor is 1.25.12.

### Upgrade notes

- Database migrations run automatically and are forward-only. Back up the
  database and master key before starting this version.
- Stop SQLite deployments during upgrade. Deploy PostgreSQL instances one at a
  time; this release does not promise mixed-version rolling compatibility.
- Read [the beta upgrade guide](docs/upgrading-to-beta.md) before replacing an
  alpha deployment.

## [0.1.0-alpha.1] - 2026-07-28

Initial public alpha with a local gateway, encrypted provider credentials,
budgeted runs and leases, append-only spend ledger, dashboard, and SDK helpers.

[0.2.0-beta.1]: https://github.com/premhiru/spendlease/compare/v0.1.0-alpha.1...v0.2.0-beta.1
[0.1.0-alpha.1]: https://github.com/premhiru/spendlease/releases/tag/v0.1.0-alpha.1
