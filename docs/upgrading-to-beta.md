# Upgrade to `v0.2.0-beta.1`

This release is a forward-only upgrade from `v0.1.0-alpha.1`. It keeps existing
ledger hashes valid, but it adds database columns and tables that the alpha
binary does not understand. Do not downgrade an upgraded database.

## Before the maintenance window

1. Record the exact alpha image digest or binary checksum.
2. Back up the database and the matching master key separately.
3. Run `spendlease ledger verify` with the alpha binary and retain the result.
4. Prepare an explicit master-key source. Production mode no longer creates a
   key beside the database. A mounted secret file or secret-manager command is
   safer than a long-lived environment variable.
5. Keep `SPENDLEASE_ADMIN_TOKEN` temporarily if an existing orchestrator uses
   it. The beta accepts it as the `legacy-admin` identity while you create named
   operators.

For SQLite, stop the alpha process before copying the database or starting the
beta. For PostgreSQL, deploy one beta instance at a time, but do not serve alpha
and beta instances concurrently; mixed-version rolling upgrades are not yet a
supported contract.

## Start and verify the beta

Start the digest-pinned beta with `SPENDLEASE_ENV=production`, the existing
store, and the existing master key. Then check:

```bash
curl --fail http://127.0.0.1:4000/healthz
curl --fail http://127.0.0.1:4000/readyz
spendlease ledger verify --store /var/lib/spendlease/spendlease.db
```

Open the dashboard and send one low-budget request through a test lease. Check
that the request appears in Recent events and that ledger export still works.

## Move from the shared admin token

Create the first named admin while the legacy credential is still configured:

```bash
spendlease keys operator create --name alice --role admin \
  --store /var/lib/spendlease/spendlease.db
```

Store the `slo_...` token immediately; it is shown once. Update dashboard and
API clients, verify the operator audit log, then remove
`SPENDLEASE_ADMIN_TOKEN` and restart every instance.

## Account for export changes

JSON ledger exports now have schema version 2. They retain the aggregate
`input_tokens` and `output_tokens` fields and add `usage`, `external_id`,
`pricing_revision`, `price_effective`, and `hash_version`. CSV consumers must
accept the corresponding new columns.

Rows written by the alpha remain hash version 1. They can only reconstruct
aggregate input and output tokens; cached-input detail was not stored by the
alpha. New rows are hash version 2 and preserve every priced token dimension.

## Rollback

Do not point the alpha binary at an upgraded database. If the beta cannot be
validated, stop it and restore the pre-upgrade database and matching master key
as a pair. Keep the failed upgraded copy for investigation.
