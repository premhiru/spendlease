# CLI reference

Run `spendlease help` for the top-level command list and
`spendlease <command> -h` for command flags. Key-management help is attached to
the final action, for example `spendlease keys lease issue -h`.

## Gateway

### `spendlease serve`

Starts the proxy, dashboard, reservation sweeper, and selected datastore.

| Flag | Default | Description |
|---|---|---|
| `--addr` | `:4000` | Listen address. |
| `--store` | `SPENDLEASE_STORE` or `./spendlease.db` | SQLite path or PostgreSQL DSN. |
| `--admin-token` | `SPENDLEASE_ADMIN_TOKEN` | Credential for non-loopback dashboard and admin access. |
| `--pricing` | embedded book | Directory containing price-book YAML. |
| `--default-run-budget` | `10.00` | Budget for implicit runs used by principal-key compatibility requests. |
| `--reservation-ttl` | `15m` | Maximum pending hold lifetime. |
| `--reservation-sweep-interval` | `30s` | Expired-hold scan interval. |
| `--openai-url` | `https://api.openai.com` | OpenAI upstream base URL. Useful for tests and private compatible endpoints. |
| `--anthropic-url` | `https://api.anthropic.com` | Anthropic upstream base URL. |
| `--kimi-url` | `https://api.moonshot.ai` | Kimi upstream base URL. |
| `--deepseek-url` | `https://api.deepseek.com` | DeepSeek upstream base URL. |
| `--xai-url` | `https://api.x.ai` | xAI upstream base URL. |
| `--gemini-url` | `https://generativelanguage.googleapis.com` | Gemini upstream base URL. |
| `--zai-url` | `https://api.z.ai` | Z.AI upstream base URL. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. |

### `spendlease demo`

Runs an in-memory gateway and mock provider with three simulated agents.

| Flag | Default | Description |
|---|---|---|
| `--duration` | `30s` | Runtime. Set `0` to run until interrupted. |
| `--target` | `http://localhost:4000` | Dashboard listen URL. |

The demo never reads the persistent store or a vendor key.

Every command that accepts `--store` accepts either a SQLite path or a
`postgres://`/`postgresql://` DSN. Use the same value for the gateway and all
management commands. PostgreSQL storage requires `SPENDLEASE_MASTER_KEY` even
outside production mode; spendlease never writes a key file beside a DSN.

### `spendlease version`

Prints the version, commit, build timestamp, Go version, and target platform.

## Principals

```bash
spendlease keys principal create --name NAME [--mode observe|enforce] [--store PATH]
spendlease keys principal list [--store PATH]
spendlease keys principal set-mode --name NAME --mode observe|enforce [--store PATH]
```

`create` prints the new `slk_` principal key once. Agent applications should
use leases instead. `set-mode` accepts the principal name, not its ID.

## Vendor credentials

```bash
spendlease keys provider set PROVIDER [--key VALUE] [--store PATH]
spendlease keys provider list [--store PATH]
spendlease keys provider rm PROVIDER [--store PATH]
```

`PROVIDER` is currently `openai`, `anthropic`, `kimi`, `deepseek`, `xai`,
`gemini`, or `zai`. Omitting `--key` makes `set`
read the value from standard input. `list` prints provider names only; it never
prints stored values.

Every provider command resolves the same master key as `serve`. A command
using a different store path or `SPENDLEASE_MASTER_KEY` will not modify the
gateway's credential vault.

## Runs

```bash
spendlease keys run create \
  --principal NAME_OR_ID \
  --budget USD \
  [--parent RUN_ID] \
  [--store PATH]
```

The budget is an exact non-negative USD decimal. `0` means no run ceiling. A
child run cannot escape a budget on its parent or another ancestor.

Run listing, closing, and live remaining-budget checks are available through
the [JSON operator API](api-reference.md#json-operator-api) and both SDK admin
clients. The CLI keeps run creation available for local bootstrap work.

## Leases

```bash
spendlease keys lease issue \
  --run RUN_ID \
  [--ttl 15m] \
  [--providers openai,anthropic] \
  [--ceiling USD] \
  [--store PATH]
```

The command prints an `sll_` token once. An empty provider list does not add a
provider restriction. A zero ceiling inherits the run budget. Lease TTL must
be positive.

Lease metadata can be listed, and one lease can be revoked, through the
[JSON operator API](api-reference.md#json-operator-api) and both SDK admin
clients. Plaintext tokens are never stored or returned by list operations.

## Revocation

```bash
spendlease keys revoke --all [--principal NAME_OR_ID] [--store PATH]
```

`--all` is required as a safeguard. Without `--principal`, the command revokes
every current lease in the store. With it, only leases belonging to that
principal are revoked.

## Master key generation

```bash
spendlease keys master generate
```

The 64-character hexadecimal key is written to standard output. Store it in a
secret manager and provide it as `SPENDLEASE_MASTER_KEY`.

## Ledger

Verify the complete append-only hash chain:

```bash
spendlease ledger verify [--store PATH]
```

Export stable JSON or CSV to standard output:

```bash
spendlease ledger export \
  [--store PATH] \
  [--format json|csv] \
  [--run RUN_ID] \
  [--principal PRINCIPAL_ID] \
  [--since RFC3339]
```

Money is emitted as exact decimal USD strings. JSON includes schema `version`
and wraps the records in an `entries` object; CSV includes a header. Both formats include `prev_hash` and
`hash`. Redirect standard output to a file when retaining an audit export:

```bash
spendlease ledger export --format csv --run run_... > ledger.csv
```

## Environment variables

| Variable | Read by | Purpose |
|---|---|---|
| `SPENDLEASE_MASTER_KEY` | CLI and gateway | AES-256 key used for vendor credentials. Required in production. |
| `SPENDLEASE_ENV` | CLI and gateway | Set to `production` to disable automatic development-key creation. |
| `SPENDLEASE_ADMIN_TOKEN` | gateway | Protects non-loopback dashboard and admin requests. |
| `SPENDLEASE_STORE` | CLI and gateway | Default SQLite path or PostgreSQL DSN; overridden by `--store`. |
| `SPENDLEASE_LEASE_TOKEN` | Python and TypeScript helpers | Lease placed in vendor-client authentication options. |
| `SPENDLEASE_URL` | Python and TypeScript helpers | Gateway URL; defaults to `http://localhost:4000`. |

Vendor variables such as `OPENAI_API_KEY` are not read by the gateway. Store
vendor keys explicitly with `spendlease keys provider set`.

## Exit codes

| Code | Meaning |
|---:|---|
| `0` | Command completed successfully, including help output. |
| `1` | The command ran but failed. |
| `2` | Invalid command or flags. |
