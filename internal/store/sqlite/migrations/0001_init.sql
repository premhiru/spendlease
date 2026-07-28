-- Initial schema: the four objects plus the append-only ledger.
--
-- Every table is STRICT, so SQLite enforces column types instead of silently
-- coercing them. A budget that arrives as the text "25.00" should fail loudly,
-- not be stored as a string that sorts wrongly.
--
-- All money is INTEGER nanodollars. All timestamps are TEXT in RFC 3339 with
-- nanosecond precision, normalised to UTC, which sorts lexicographically in
-- the same order it sorts chronologically.

CREATE TABLE principals (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    -- SHA-256 hex of the slk_ key. The plaintext is shown once and never stored.
    key_hash   TEXT NOT NULL UNIQUE,
    mode       TEXT NOT NULL CHECK (mode IN ('observe', 'enforce')),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE runs (
    id            TEXT PRIMARY KEY,
    principal_id  TEXT NOT NULL REFERENCES principals(id),
    -- NULL for a root run. A child draws from its parent's remaining budget;
    -- that is the entire delegation model.
    parent_run_id TEXT REFERENCES runs(id),
    budget_nanos  INTEGER NOT NULL CHECK (budget_nanos >= 0),
    status        TEXT NOT NULL CHECK (status IN ('active', 'closed')),
    created_at    TEXT NOT NULL,
    closed_at     TEXT,
    -- A run cannot be its own parent. Deeper cycles are prevented in the
    -- store, since SQLite cannot express a recursive check here.
    CHECK (parent_run_id IS NULL OR parent_run_id <> id),
    CHECK ((status = 'closed') = (closed_at IS NOT NULL))
) STRICT;

CREATE INDEX runs_by_principal ON runs (principal_id, created_at DESC);
CREATE INDEX runs_by_parent ON runs (parent_run_id) WHERE parent_run_id IS NOT NULL;

CREATE TABLE leases (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES runs(id),
    -- SHA-256 hex of the sll_ token.
    token_hash    TEXT NOT NULL UNIQUE,
    -- Comma-separated provider scope. Empty means every configured provider.
    providers     TEXT NOT NULL DEFAULT '',
    ceiling_nanos INTEGER NOT NULL CHECK (ceiling_nanos >= 0),
    expires_at    TEXT NOT NULL,
    revoked_at    TEXT,
    created_at    TEXT NOT NULL
) STRICT;

CREATE INDEX leases_by_run ON leases (run_id, created_at DESC);

CREATE TABLE reservations (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL REFERENCES runs(id),
    lease_id     TEXT NOT NULL REFERENCES leases(id),
    amount_nanos INTEGER NOT NULL CHECK (amount_nanos >= 0),
    status       TEXT NOT NULL CHECK (status IN ('pending', 'settled', 'released', 'expired')),
    expires_at   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    resolved_at  TEXT,
    CHECK ((status = 'pending') = (resolved_at IS NULL))
) STRICT;

-- Both hot paths are over pending rows only: summing a run's outstanding
-- holds, and finding holds the sweeper should reclaim.
CREATE INDEX reservations_pending_by_run ON reservations (run_id) WHERE status = 'pending';
CREATE INDEX reservations_pending_by_expiry ON reservations (expires_at) WHERE status = 'pending';

CREATE TABLE ledger (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        TEXT NOT NULL REFERENCES runs(id),
    -- Denormalised from the run so per-principal totals never need a join
    -- against a run that may since have been closed.
    principal_id  TEXT NOT NULL REFERENCES principals(id),
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    input_tokens  INTEGER NOT NULL CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL CHECK (output_tokens >= 0),
    cost_nanos    INTEGER NOT NULL CHECK (cost_nanos >= 0),
    -- 1 when the cost did not come from a known price and reported usage.
    estimated     INTEGER NOT NULL CHECK (estimated IN (0, 1)),
    created_at    TEXT NOT NULL,
    prev_hash     TEXT NOT NULL,
    hash          TEXT NOT NULL UNIQUE
) STRICT;

CREATE INDEX ledger_by_run ON ledger (run_id);
CREATE INDEX ledger_by_principal ON ledger (principal_id);
CREATE INDEX ledger_by_created ON ledger (created_at);

-- The ledger is append-only, and that is enforced here rather than by
-- convention in application code. A bug, a migration, or an operator with a
-- sqlite3 prompt cannot rewrite spend history: every UPDATE and DELETE is
-- refused by the database itself.
--
-- Correcting a mistake means appending a compensating entry, exactly as a
-- paper ledger would. Retrofitting this after the first compliance-sensitive
-- user is a miserable project, so it exists from the first migration.
CREATE TRIGGER ledger_no_update
BEFORE UPDATE ON ledger
BEGIN
    SELECT RAISE(ABORT, 'ledger is append-only: UPDATE is not permitted');
END;

CREATE TRIGGER ledger_no_delete
BEFORE DELETE ON ledger
BEGIN
    SELECT RAISE(ABORT, 'ledger is append-only: DELETE is not permitted');
END;
