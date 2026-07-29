-- Encrypted vendor credentials.
--
-- One row per provider. The plaintext vendor API key never appears here: the
-- ciphertext is AES-256-GCM under the master key, and the nonce is fresh for
-- every write. The provider name is used as additional authenticated data, so
-- a ciphertext cannot be moved from one provider's row to another's without
-- decryption failing.
--
-- Deliberately not part of the ledger and deliberately mutable: rotating a
-- vendor key is a normal operation, unlike rewriting spend history.

CREATE TABLE credentials (
    provider   TEXT PRIMARY KEY,
    nonce      BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
