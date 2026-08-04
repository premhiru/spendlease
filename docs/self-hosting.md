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
- Version tags are created for GitHub releases. `v0.2.0-beta.1` is the first
  version intended for an end-to-end evaluation.

Evaluate `edge`, then deploy the corresponding `sha-...` tag rather than a
mutable tag. Source builds should likewise pin a commit.

Tagged releases attach platform binaries and checksums, Python wheel/source
archives, an npm tarball, and `container-image.txt`. The last file contains
the immutable `ghcr.io/...@sha256:...` reference for that build. Registry
packages are published through PyPI and npm trusted publishing; GitHub release
artifacts remain available even when a registry is temporarily unavailable.

## Run the container

This example uses SQLite. Create a volume for its database:

```bash
docker volume create spendlease-data
```

Generate a master key and store the output in a secret manager:

```bash
docker run --rm ghcr.io/premhiru/spendlease:edge keys master generate
```

Start the gateway on loopback. Substitute the pinned `sha-...` tag selected
above and provide the master key through your deployment's secret mechanism:

```bash
docker run -d --name spendlease \
  --restart unless-stopped \
  -p 127.0.0.1:4000:4000 \
  -v spendlease-data:/data \
  -e SPENDLEASE_ENV=production \
  -e SPENDLEASE_MASTER_KEY="$SPENDLEASE_MASTER_KEY" \
  ghcr.io/premhiru/spendlease:sha-...
```

Check the process before adding credentials:

```bash
curl http://localhost:4000/healthz
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
`SPENDLEASE_MASTER_KEY`, and any admin token through its credential or secret
facility rather than a world-readable environment file.

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
`SPENDLEASE_MASTER_KEY`. Generate a key with:

```bash
spendlease keys master generate
```

Development mode creates a key beside a SQLite database when the environment
variable is absent. Production mode and PostgreSQL both refuse to do this:

```bash
export SPENDLEASE_ENV=production
export SPENDLEASE_MASTER_KEY=<64-hex-character-key>
```

Back up the master key separately from the database. Losing it makes stored
vendor credentials unreadable. Master-key rotation is not implemented; to
change keys, stop the gateway, provide the new key, and re-enter every vendor
credential before restarting traffic. Keep a tested backup until the new
credentials have been verified.

## Dashboard and admin access

Credential-free dashboard access requires both a loopback TCP peer and a
loopback `Host` such as `localhost:4000`. This prevents a same-host reverse
proxy or DNS-rebound hostname from inheriting local trust. Other requests are
refused unless an admin token is configured:

```bash
SPENDLEASE_ADMIN_TOKEN="a-long-random-secret" \
  spendlease serve --addr 0.0.0.0:4000
```

`--admin-token` overrides the environment variable. Scripts can send
`Authorization: Bearer <token>`. State-changing requests must also send
`X-Spendlease-Admin: 1`; both bundled SDK clients do so. Browsers use HTTP
Basic authentication with any username and the token as the password. The
dashboard supplies the mutation header and rejects cross-origin writes.

The admin token does not encrypt traffic. Put TLS at a trusted reverse proxy
in front of any remotely reachable gateway, restrict network access, and do
not expose port 4000 directly to the public internet.

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

Before replacing a binary or container:

1. Read the release notes and back up the database.
2. For SQLite, stop the old process before starting the new one. PostgreSQL
   serializes migrations, but deploy one version at a time until release notes
   explicitly say a rolling upgrade is compatible.
3. Start the pinned new version against the existing datastore.
4. Check `/healthz`, the dashboard, and one low-budget request.

There is not yet a guard against opening a database created by a newer binary.
Keep the backup until the upgraded deployment has been verified. Downgrades
are not supported.
