CREATE TABLE principals (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    key_hash TEXT NOT NULL UNIQUE,
    mode TEXT NOT NULL CHECK (mode IN ('observe', 'enforce')),
    created_at TEXT NOT NULL
);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL REFERENCES principals(id),
    parent_run_id TEXT REFERENCES runs(id),
    budget_nanos BIGINT NOT NULL CHECK (budget_nanos >= 0),
    status TEXT NOT NULL CHECK (status IN ('active', 'closed')),
    created_at TEXT NOT NULL,
    closed_at TEXT,
    CHECK (parent_run_id IS NULL OR parent_run_id <> id),
    CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);
CREATE INDEX runs_by_principal ON runs (principal_id, created_at DESC);
CREATE INDEX runs_by_parent ON runs (parent_run_id) WHERE parent_run_id IS NOT NULL;

CREATE TABLE leases (
    event_order BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    token_hash TEXT NOT NULL UNIQUE,
    providers TEXT NOT NULL DEFAULT '',
    ceiling_nanos BIGINT NOT NULL CHECK (ceiling_nanos >= 0),
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX leases_by_run ON leases (run_id, created_at DESC);

CREATE TABLE reservations (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    lease_id TEXT REFERENCES leases(id),
    amount_nanos BIGINT NOT NULL CHECK (amount_nanos >= 0),
    status TEXT NOT NULL CHECK (status IN ('pending', 'settled', 'released', 'expired')),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    resolved_at TEXT,
    CHECK ((status = 'pending') = (resolved_at IS NULL))
);
CREATE INDEX reservations_pending_by_run ON reservations (run_id) WHERE status = 'pending';
CREATE INDEX reservations_pending_by_expiry ON reservations (expires_at) WHERE status = 'pending';

CREATE TABLE ledger (
    seq BIGINT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    principal_id TEXT NOT NULL REFERENCES principals(id),
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens BIGINT NOT NULL CHECK (input_tokens >= 0),
    output_tokens BIGINT NOT NULL CHECK (output_tokens >= 0),
    cost_nanos BIGINT NOT NULL CHECK (cost_nanos >= 0),
    estimated INTEGER NOT NULL CHECK (estimated IN (0, 1)),
    created_at TEXT NOT NULL,
    prev_hash TEXT NOT NULL,
    hash TEXT NOT NULL UNIQUE
);
CREATE INDEX ledger_by_run ON ledger (run_id);
CREATE INDEX ledger_by_principal ON ledger (principal_id);
CREATE INDEX ledger_by_created ON ledger (created_at);

CREATE FUNCTION reject_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger is append-only: % is not permitted', TG_OP
        USING ERRCODE = '55000';
END;
$$;
CREATE TRIGGER ledger_no_update BEFORE UPDATE ON ledger
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();
CREATE TRIGGER ledger_no_delete BEFORE DELETE ON ledger
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE TABLE credentials (
    provider TEXT PRIMARY KEY,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE reservation_settlements (
    reservation_id TEXT PRIMARY KEY REFERENCES reservations(id),
    ledger_seq BIGINT NOT NULL UNIQUE REFERENCES ledger(seq),
    created_at TEXT NOT NULL
);

CREATE TABLE budget_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    principal_id TEXT NOT NULL REFERENCES principals(id),
    run_id TEXT NOT NULL REFERENCES runs(id),
    lease_id TEXT REFERENCES leases(id),
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    enforced INTEGER NOT NULL CHECK (enforced IN (0, 1)),
    requested_nanos BIGINT NOT NULL CHECK (requested_nanos >= 0),
    remaining_nanos BIGINT NOT NULL CHECK (remaining_nanos >= 0),
    shortfall_nanos BIGINT NOT NULL CHECK (shortfall_nanos >= 0),
    created_at TEXT NOT NULL
);
CREATE INDEX budget_events_by_principal ON budget_events (principal_id, created_at DESC);
CREATE INDEX budget_events_recent ON budget_events (created_at DESC);
