# 7. The credential vault, and where the master key comes from

- **Status:** Accepted
- **Date:** 2026-07-28

## Context

The phase plan never assigns the credential vault to a phase, but the gateway cannot proxy anything without a vendor key to attach at egress. The choice was to ship the gateway reading vendor keys from environment variables and add encryption later, or to build the vault now.

`SECURITY.md` is already published and already promises that vendor keys are encrypted at rest with AES-256-GCM. Shipping environment-variable credentials would have made a public security claim false. That settled it.

The harder question is where the master key comes from. Two requirements point in opposite directions:

- The 60-second quickstart is `docker run` with **no configuration**. Demanding a master key before anything works would destroy it.
- A key generated automatically and stored next to the data it protects is not a secret. It is obfuscation with extra steps.

## Decision

The vault encrypts each vendor key with AES-256-GCM under a 32-byte master key, with a fresh nonce per write, and stores only the ciphertext.

**The provider name is used as additional authenticated data.** It is not secret; binding it means a ciphertext cannot be moved between rows. Copying OpenAI's encrypted key into Anthropic's row makes decryption fail rather than silently returning the wrong vendor's credential.

The master key is resolved in this order:

1. `SPENDLEASE_MASTER_KEY`, if set.
2. A `.key` file beside the database, if it exists.
3. A freshly generated key, written to that file with owner-only permissions.

**Steps 2 and 3 are refused when `SPENDLEASE_ENV=production`**, and the process fails to start with an error naming the variable and the command that generates a key. That is the resolution of the tension above: convenience is the default, and it is explicitly unavailable exactly where it would be dangerous.

`MasterKey.String()` deliberately does not render the key, so a stray `%v` or a struct dump cannot leak it. Only `Hex()` produces the real value, and only the key file and `keys master generate` call it.

## Consequences

- `docker run` with no configuration still works, and vendor keys are genuinely encrypted at rest from the first release.
- A production deployment that forgets the master key fails loudly at startup rather than quietly encrypting credentials under a key sitting beside them.
- Losing the master key means losing every stored vendor credential. They must be re-entered, not recovered. The error on a decryption failure says exactly this, because "the master key does not match this database" is the only useful thing to tell an operator in that moment.
- Rotating the master key is not implemented. Doing it properly means decrypting under the old key and re-encrypting under the new one in a single transaction, and there is no interface for supplying two keys at once. This is a real gap and should be closed before anyone depends on rotation.
- The vault landing here rather than in its own phase makes this PR larger than the phase plan implies. Recorded here rather than done silently.

## Options rejected

- **Vendor keys from environment variables.** Simplest, and would have contradicted a published promise in `SECURITY.md`. It also scales badly: rotating a key means restarting the process, and there is nowhere to record when a credential was last changed.
- **Require `SPENDLEASE_MASTER_KEY` always.** Honest and safe, and it costs the zero-configuration quickstart, which is the single strongest thing this product has.
- **Derive the master key from a passphrase with a KDF.** Adds a prompt or another variable without removing the underlying problem of where the secret lives.
- **Store credentials in an external secret manager.** Correct for a large deployment, wrong as a hard dependency for a single-container product whose selling point is that there is nothing to provision.
