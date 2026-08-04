# ADR-0020: External key sources and transactional rotation

**Status:** Accepted

**Amends:** ADR-0007. Its encryption and development-key decisions remain;
this record adds production sources and closes the rotation gap.

## Context

A production process should not require its master key in a command line or a
file baked into an image. Operators already use cloud secret managers, KMS
envelope encryption, Vault, Docker secrets, Kubernetes Secrets, and Secrets
Store CSI drivers. Native SDKs for every provider would add large dependency
trees, credential discovery behavior, release cadence, and network policy to
the credential boundary.

Changing the master key also cannot mean re-entering every vendor credential.
Re-encrypting rows one at a time would leave a mixed vault if a process fails,
and changing all ciphertext instantly would break replicas that still hold
the old key.

## Decision

The primary key has three mutually exclusive sources: a direct environment
value, a mounted file, or a JSON array describing an executable and arguments.
The command runs without a shell, inherits the process environment for
workload identity, times out after 15 seconds, discards stderr, and limits
retained stdout to 4 KiB. Its stdout must be only the 64-character hexadecimal
key. The file and command adapters let platform-native secret delivery or a
small operator-owned KMS wrapper integrate without cloud SDKs in spendlease.

A keyring has one primary write key and at most one temporary previous read
key. New credentials always use the primary. Reads try the primary and then
the previous key, allowing every gateway replica to receive the two-key
configuration before stored ciphertext changes.

Master-key rotation asks the store to transform every credential inside one
transaction. The store reads and locks the complete credential set, the vault
decrypts each row with the keyring and encrypts it with a fresh nonce under the
primary key, and the store commits only after every row succeeds. SQLite uses
an in-process credential lock. PostgreSQL combines that with a
transaction-scoped advisory lock across processes.

The operator procedure is deliberately staged: deploy new-primary plus
old-previous, verify, rotate transactionally, verify again, remove old, and
verify with new-only. The CLI requires an explicit `--confirm` for the mutation.

## Consequences

- Secret-manager and KMS integrations can use standard environment injection,
  mounted files, or a narrowly scoped wrapper executable.
- spendlease does not gain cloud SDK credentials, provider-specific network
  clients, or a shell injection surface.
- Conflicting key sources fail startup instead of relying on precedence.
- A bad or tampered credential aborts the complete rotation.
- Old-only replicas cannot read new ciphertext. Operators must complete the
  staged two-key deployment before running rotation.
- Command integrations must provide their own observability because stderr is
  intentionally not retained or echoed by spendlease.

## Rejected alternatives

**One native SDK per cloud.** This expands the most sensitive dependency and
configuration surface while still excluding other secret managers.

**A shell command string.** Pipes are convenient, but shell parsing creates an
unnecessary injection and quoting boundary. A JSON argv array is explicit and
portable.

**Update credentials one row at a time.** A crash or one undecryptable row
would leave the database dependent on two keys indefinitely.

**Switch directly from old to new.** This creates an outage window across
replicas and makes rollback ambiguous once any new ciphertext exists.
