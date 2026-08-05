# Self-hosting

`spendlease` supports a single-process SQLite deployment and multi-instance
PostgreSQL deployments. This page covers persistent state, the master key,
remote dashboard access, backups, and upgrades.

SQLite is the simplest place to begin. Choose PostgreSQL when more than one
gateway process must share live state, when your platform already operates a
managed database, or when database-native backup and failover are required.

## Choose a build

The container registry publishes three useful kinds of tag:

- `edge` follows `main` and may change at any time.
- `sha-<commit>` identifies one immutable build.
- Version tags are created for GitHub releases. Container tag
  `0.2.0-beta.1` corresponds to the current `v0.2.0-beta.1` release.

For the current beta, deploy the digest from the release's
`container-image.txt`. Use `edge` only to evaluate unreleased `main`, then pin
the corresponding `sha-...` tag. Source builds should likewise pin a commit.

Tagged releases attach platform binaries and checksums, SPDX SBOMs, signed
provenance and SBOM bundles, Python wheel/source archives, an npm tarball, and
`container-image.txt`. The last file contains the immutable
`ghcr.io/...@sha256:...` reference for that build. Registry packages are
published through PyPI and npm trusted publishing; GitHub release artifacts
remain available even when a registry is temporarily unavailable.

Verify a downloaded binary before running it:

```bash
sha256sum -c spendlease_v0.2.0-beta.1_linux_amd64.sha256
gh attestation verify spendlease_v0.2.0-beta.1_linux_amd64 \
  --repo premhiru/spendlease \
  --signer-workflow premhiru/spendlease/.github/workflows/release.yml \
  --source-ref refs/tags/v0.2.0-beta.1
```

The second command verifies signed SLSA provenance through GitHub. To verify
the release-provided bundle instead, pass
`--bundle spendlease_...provenance.sigstore.json`. For a fully offline check,
obtain and retain a trusted root ahead of time with
`gh attestation trusted-root > trusted_root.jsonl`, then also pass
`--custom-trusted-root trusted_root.jsonl`.

Use the digest from `container-image.txt` for the same check against the image:

```bash
gh attestation verify \
  oci://ghcr.io/premhiru/spendlease@sha256:... \
  --repo premhiru/spendlease \
  --signer-workflow premhiru/spendlease/.github/workflows/release.yml \
  --source-ref refs/tags/v0.2.0-beta.1
```

## Run the container

This example uses SQLite. Create a volume for its database:

```bash
docker volume create spendlease-data
```

Load the immutable image reference published with the beta:

```bash
SPENDLEASE_IMAGE=$(curl -fsSL \
  https://github.com/premhiru/spendlease/releases/download/v0.2.0-beta.1/container-image.txt)
```

Generate a master key and store the output in a secret manager:

```bash
docker run --rm "$SPENDLEASE_IMAGE" keys master generate
```

Start the gateway on loopback and provide the master key through your
deployment's secret mechanism:

```bash
docker run -d --name spendlease \
  --restart unless-stopped \
  -p 127.0.0.1:4000:4000 \
  -v spendlease-data:/data \
  -e SPENDLEASE_ENV=production \
  -e SPENDLEASE_MASTER_KEY="$SPENDLEASE_MASTER_KEY" \
  "$SPENDLEASE_IMAGE"
```

Check the process before adding credentials:

```bash
curl http://localhost:4000/healthz
curl http://localhost:4000/readyz
```

Run management commands inside the container so they use the same database
and master key as the gateway:

```bash
docker exec spendlease spendlease keys principal create --name checkout-agent --store /data/spendlease.db
printf '%s' "$OPENAI_API_KEY" | docker exec -i spendlease spendlease keys provider set openai --store /data/spendlease.db
docker exec spendlease spendlease keys run create --principal checkout-agent --budget 25.00 --store /data/spendlease.db
```

Copy the `run_...` ID from the last command and issue a lease:

```bash
docker exec spendlease spendlease keys lease issue --run run_... --ttl 15m --providers openai --store /data/spendlease.db
```

## Run the binary

The binary creates and migrates the database on first start:

```bash
spendlease serve --store /var/lib/spendlease/spendlease.db
```

Run every `spendlease keys ...` command with the same `--store` path. For a
service manager such as systemd, provide `SPENDLEASE_ENV=production`,
the master-key source, and any temporary legacy admin token through its
credential or secret facility rather than a world-readable environment file.

## PostgreSQL

Create a dedicated database and role using your provider's normal workflow.
The role needs permission to create tables, indexes, functions, and triggers
in its target schema. Require TLS for any connection that leaves a private
host or network.

Pass the DSN as `--store` and provide the master key explicitly:

```bash
export SPENDLEASE_ENV=production
export SPENDLEASE_MASTER_KEY=<64-hex-character-key>
export SPENDLEASE_STORE='postgres://spendlease:password@db.example/spendlease?sslmode=require'

spendlease serve
```

Use the same DSN and master key for management commands:

```bash
spendlease keys principal create --name checkout-agent
printf '%s' "$OPENAI_API_KEY" | \
  spendlease keys provider set openai
```

The DSN may also use the `postgresql://` scheme. Percent-encode reserved
characters in usernames and passwords. Credentials are redacted from gateway
logs. Prefer injecting `SPENDLEASE_STORE` from a secret manager instead of
placing the DSN in a command line, shell history, image, or source file.

Migrations are embedded and protected by a PostgreSQL advisory lock, so
replicas may start together. Reservation decisions use a transaction-scoped
lock per principal, which prevents two replicas from authorizing the same
remaining budget while allowing unrelated principals to proceed in parallel.
Ledger appends use a separate transaction-scoped lock so the hash chain has
one unambiguous head across the deployment.

The built-in pool defaults to 20 open and idle connections per process, with a
30-minute maximum connection lifetime. Include that multiplication when sizing
the database connection limit. Current pool settings are fixed defaults; CLI
pool tuning is a later production-hardening item.

## Master key

Vendor credentials are encrypted with AES-256-GCM under
one primary master key. Generate a key with:

```bash
spendlease keys master generate
```

Configure exactly one primary source:

- `SPENDLEASE_MASTER_KEY` contains the 64 hexadecimal characters directly.
- `SPENDLEASE_MASTER_KEY_FILE` names a mounted secret file. This works with
  Docker secrets, Kubernetes Secrets, Secrets Store CSI drivers, and managed
  platforms that project a secret into the filesystem.
- `SPENDLEASE_MASTER_KEY_COMMAND` is a JSON array containing an executable and
  its arguments. Its standard output must be only the key. The command runs
  directly, without a shell, has a 15-second timeout, discards standard error,
  and retains no more than 4 KiB of output.

For example, a local wrapper can retrieve a secret or ask a KMS to decrypt an
envelope-encrypted key:

```bash
export SPENDLEASE_MASTER_KEY_COMMAND='["/usr/local/bin/read-spendlease-master-key"]'
```

Use an absolute executable path, keep cloud credentials in the platform's
workload identity, and make the wrapper print no labels or JSON. Arguments are
not passed through a shell, so pipes, redirects, variable expansion, and
quoted command strings do not work. Put that logic inside the wrapper when it
is needed.

Development mode creates a key beside a SQLite database when no explicit
source is configured. Production mode and PostgreSQL both refuse to do this:

```bash
export SPENDLEASE_ENV=production
export SPENDLEASE_MASTER_KEY=<64-hex-character-key>
```

Back up the master key separately from the database. Losing it makes stored
vendor credentials unreadable.

### Rotate the master key

Rotation is staged so running replicas never face a half-rotated vault:

1. Generate and store the new key. Keep the old key available and retain a
   tested database backup.
2. Configure the new key through one primary source above. Configure the old
   key through exactly one matching fallback source:
   `SPENDLEASE_PREVIOUS_MASTER_KEY`,
   `SPENDLEASE_PREVIOUS_MASTER_KEY_FILE`, or
   `SPENDLEASE_PREVIOUS_MASTER_KEY_COMMAND`.
3. Deploy that two-key configuration to every gateway. New writes use the new
   primary key; reads accept either key.
4. Verify that all ciphertext is readable, then rotate it in one datastore
   transaction:

   ```bash
   spendlease keys master verify
   spendlease keys master rotate --confirm
   spendlease keys master verify
   ```

5. Remove the previous-key source and restart every gateway. Run `verify` once
   more with only the new primary configured.

If any credential cannot be decrypted, rotation rolls back without changing
another row. SQLite serializes the transaction in-process. PostgreSQL also
uses a database advisory lock, so credential writes and rotation commands from
other replicas cannot interleave. Do not remove the old key until the final
verification and backup recovery drill succeed.

## Dashboard and admin access

Credential-free dashboard access requires both a loopback TCP peer and a
loopback `Host` such as `localhost:4000`. This prevents a same-host reverse
proxy or DNS-rebound hostname from inheriting local trust. Create a named
admin before exposing the gateway on a network interface:

```bash
spendlease keys operator create --name alice --role admin
spendlease serve --addr 0.0.0.0:4000
```

The command prints an `slo_` token once. Scripts send it as
`Authorization: Bearer <token>`. Browsers use HTTP Basic authentication with
the operator name as username and token as password. State-changing requests
must also send `X-Spendlease-Admin: 1`; both bundled SDK clients do so. The
dashboard supplies that header and rejects cross-origin writes.

Roles are cumulative. `viewer` is read-only, `operator` can create and close
runs and issue or revoke leases, and `admin` can also switch enforcement and
activate a principal-wide kill switch. The dashboard shows the signed-in name
and role and hides admin controls from lower roles.

`SPENDLEASE_ADMIN_TOKEN` and `--admin-token` still work as a shared
`legacy-admin` identity for upgrades from an older release. Startup logs a
warning when either is used. Create named operators, update clients, then
remove the shared token.

Operator tokens do not encrypt traffic. Put TLS at a trusted reverse proxy in
front of any remotely reachable gateway, restrict network access, and do not
expose port 4000 directly to the public internet.

Every authenticated HTTP mutation writes an immutable attempt record before
the handler runs and a result record afterward. If the attempt cannot be
stored, the request fails with `503` without running the mutation. Inspect the
trail with `spendlease keys operator audit` or `GET /api/v1/operator-audit`.

## Health, metrics, and alerts

Use `/healthz` only for process liveness. It deliberately performs no I/O and
stays healthy during a datastore outage. Use `/readyz` for load-balancer and
orchestrator readiness; it returns `503` if the datastore does not answer
within two seconds. The gateway also decrypts every stored vendor credential
once during startup, so a mismatched master key prevents a process from
becoming available.

`/metrics` serves Prometheus text without authentication. Its labels are
limited to surface, status class, configured provider, mode, and outcome. It
does not expose principal or run IDs, model names, request paths, prompts, or
credentials. Restrict the operational port at the network layer even though
the payload is aggregate.

The main series are:

- `spendlease_http_requests_total`;
- `spendlease_http_request_duration_seconds_sum`;
- `spendlease_http_response_bytes_total`;
- `spendlease_budget_decisions_total`; and
- `spendlease_alert_delivery_total`.

An optional webhook reports `budget_blocked`, `budget_would_block`,
`budget_decision_error`, `spend_unenforceable`, `upstream_error`, and
`audit_result_failed` events:

```bash
export SPENDLEASE_ALERT_WEBHOOK=https://alerts.example/spendlease
export SPENDLEASE_ALERT_WEBHOOK_SECRET=<random-secret>
spendlease serve
```

Events are small `v1` JSON objects with an `alt_` event ID, UTC timestamp,
type, and sanitized identifiers relevant to the failure. Delivery uses a
128-event memory queue, never blocks the agent request, retries three times,
and records sent, failed, or dropped totals in metrics. The queue drains for
up to ten seconds during graceful shutdown.

The raw JSON body is signed as
`X-Spendlease-Signature: sha256=<hex HMAC-SHA256>`. Verify that header before
parsing the event. Webhook redirects are refused. Production mode requires an
HTTPS URL and a signing secret; local development may use HTTP for a receiver
on loopback.

The server defaults to 256 concurrent proxied requests. Excess requests fail
quickly with `503` and `Retry-After: 1`; health, readiness, metrics, and the
dashboard remain reachable. Request reads, vendor connects, TLS handshakes,
response headers, and non-streaming requests all have explicit deadlines.
Streaming responses have no total write deadline, because a valid completion
may remain open for minutes. Tune the corresponding `serve` flags only after
measuring vendor latency and datastore capacity.

## SQLite behavior

Migrations are embedded and run at startup, one transaction at a time. The
database uses foreign keys, write-ahead logging, a busy timeout, and
`synchronous=NORMAL`. The pure-Go SQLite driver keeps the binary independent
of cgo and libc.

All management commands can safely open the same database while the gateway
is running. Do not place the SQLite file on a network filesystem whose locking
semantics are incompatible with SQLite.

## Backups

For SQLite, the simplest backup is an offline copy:

1. Stop the gateway.
2. Copy `spendlease.db` and any `spendlease.db-wal` and
   `spendlease.db-shm` companions.
3. Start the gateway again.

For an online backup, use SQLite's backup API or run the following through a
SQLite client connected to the live database:

```sql
VACUUM INTO 'spendlease-backup.db';
```

For PostgreSQL, use the automated backup and point-in-time recovery facilities
provided by your database service. For a portable logical backup, use
`pg_dump` with the same DSN:

```bash
pg_dump --format=custom --file=spendlease.dump "$SPENDLEASE_STORE"
```

Restore into a separate database and run `spendlease ledger verify` against
the restored DSN as part of every recovery drill.

The hash chain detects changes to ledger rows; it does not replace backups.
Test both database restoration and access to the matching master key.

Verify the live database after a backup or before an upgrade:

```bash
spendlease ledger verify --store /var/lib/spendlease/spendlease.db
spendlease ledger export --store /var/lib/spendlease/spendlease.db \
  --format csv > spendlease-ledger.csv
```

## Upgrades

Deployments moving from the public alpha should follow the dedicated
[`v0.2.0-beta.1` upgrade guide](upgrading-to-beta.md).

Before replacing a binary or container:

1. Read the release notes and back up the database.
2. For SQLite, stop the old process before starting the new one. PostgreSQL
   serializes migrations, but deploy one version at a time until release notes
   explicitly say a rolling upgrade is compatible.
3. Start the pinned new version against the existing datastore.
4. Check `/healthz`, `/readyz`, the dashboard, metrics scrape, and one
   low-budget request.

There is not yet a guard against opening a database created by a newer binary.
Keep the backup until the upgraded deployment has been verified. Downgrades
are not supported.
