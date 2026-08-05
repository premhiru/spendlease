ALTER TABLE ledger ADD COLUMN hash_version INTEGER NOT NULL DEFAULT 1
    CHECK (hash_version IN (1, 2));
ALTER TABLE ledger ADD COLUMN usage_json TEXT NOT NULL DEFAULT '';
ALTER TABLE ledger ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ledger ADD COLUMN pricing_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE ledger ADD COLUMN price_effective TEXT NOT NULL DEFAULT '';

CREATE INDEX ledger_by_external_id
    ON ledger (provider, external_id)
    WHERE external_id <> '';
