# Self-hosting

Production deployment, PostgreSQL, and key management.

> [!NOTE]
> This page is incomplete. The PostgreSQL backend and key management it will
> describe do not exist yet; they arrive with the deployment work in later
> phases. What is documented below is implemented and true today.

## SQLite, the default

The default datastore needs no configuration. Point `--store` at a file path and the database is created and migrated on first start:

```bash
spendlease serve --store ./spendlease.db
```

Migrations are embedded in the binary and applied automatically at startup, in version order, each inside its own transaction — so a failure part-way leaves the database at the last complete version rather than in a half-applied state. Startup is idempotent: already-applied migrations are skipped.

The connection is opened with foreign keys enforced, write-ahead logging enabled, a busy timeout set, and `synchronous=NORMAL`. None of the first three are SQLite defaults, and all three are wrong to omit in a service.

The driver is `modernc.org/sqlite`, a pure-Go translation with no cgo and no libc dependency. That is what allows the static binary and the 8MB distroless image.

## Backups

The ledger is append-only and hash-chained, so a restored backup can be *verified* rather than merely trusted — re-running chain verification proves nothing was altered in transit or at rest.

For SQLite, either copy the database file together with its `-wal` and `-shm` companions while the service is stopped, or take a consistent snapshot of a running service with:

```sql
VACUUM INTO 'spendlease-backup.db';
```

## Dashboard and admin access

Loopback access needs no credential, preserving the zero-configuration local
quickstart. Requests from any non-loopback address are refused unless an admin
token is configured:

```bash
SPENDLEASE_ADMIN_TOKEN="a-long-random-secret" \
  spendlease serve --addr 0.0.0.0:4000
```

`--admin-token` overrides the environment variable. Remote scripts may send
`Authorization: Bearer <token>`; browsers use HTTP Basic authentication with
any username and the token as the password. Put TLS or a trusted terminating
proxy in front of the port because Basic and Bearer credentials do not encrypt
the connection.

The spend table and every admin mutation use the same guard. Static dashboard
assets are public, contain no deployment data, and are embedded in the binary;
the dashboard does not require an internet connection or CDN.

## Not yet written

- PostgreSQL setup, and the guarantee that it runs the same schema
- `SPENDLEASE_MASTER_KEY` generation, storage and rotation
- Guarding against an older binary opening a newer database
