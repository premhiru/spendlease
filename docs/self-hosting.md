# Self-hosting

`spendlease` is a single process backed by SQLite. This page covers persistent
state, the master key, remote dashboard access, backups, and upgrades.
PostgreSQL is not implemented.

## Choose a build

The container registry publishes three useful kinds of tag:

- `edge` follows `main` and may change at any time.
- `sha-<commit>` identifies one immutable build.
- Version tags are created for GitHub releases. The current
  `v0.1.0-alpha.1` release predates the complete gateway and should not be used
  with the current documentation.

Evaluate `edge`, then deploy the corresponding `sha-...` tag rather than a
mutable tag. Source builds should likewise pin a commit.

## Run the container

Create a volume for the SQLite database:

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

## Master key

Vendor credentials are encrypted with AES-256-GCM under
`SPENDLEASE_MASTER_KEY`. Generate a key with:

```bash
spendlease keys master generate
```

Development mode creates a key beside the database when the environment
variable is absent. Production mode refuses to do this:

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

Loopback dashboard access does not require a credential. A request from any
non-loopback address is refused unless an admin token is configured:

```bash
SPENDLEASE_ADMIN_TOKEN="a-long-random-secret" \
  spendlease serve --addr 0.0.0.0:4000
```

`--admin-token` overrides the environment variable. Scripts can send
`Authorization: Bearer <token>`. Browsers use HTTP Basic authentication with
any username and the token as the password.

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

The simplest backup is an offline copy:

1. Stop the gateway.
2. Copy `spendlease.db` and any `spendlease.db-wal` and
   `spendlease.db-shm` companions.
3. Start the gateway again.

For an online backup, use SQLite's backup API or run the following through a
SQLite client connected to the live database:

```sql
VACUUM INTO 'spendlease-backup.db';
```

The hash chain detects changes to ledger rows; it does not replace backups.
Test both database restoration and access to the matching master key.

## Upgrades

Before replacing a binary or container:

1. Read the release notes and back up the database.
2. Stop the old process so only one version performs startup migrations.
3. Start the pinned new version against the existing database.
4. Check `/healthz`, the dashboard, and one low-budget request.

There is not yet a guard against opening a database created by a newer binary.
Keep the backup until the upgraded deployment has been verified. Downgrades
are not supported.
