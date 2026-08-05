CREATE TABLE operators (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    token_hash  TEXT NOT NULL UNIQUE,
    role        TEXT NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    disabled_at TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;

CREATE TABLE operator_audit (
    id          TEXT PRIMARY KEY,
    request_id  TEXT NOT NULL,
    actor_id    TEXT NOT NULL,
    actor_name  TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    phase       TEXT NOT NULL CHECK (phase IN ('attempt', 'result')),
    action      TEXT NOT NULL,
    resource    TEXT NOT NULL,
    remote_addr TEXT NOT NULL,
    status_code INTEGER NOT NULL CHECK (status_code >= 0),
    created_at  TEXT NOT NULL
) STRICT;

CREATE INDEX operator_audit_recent ON operator_audit (created_at DESC, id);
CREATE INDEX operator_audit_actor ON operator_audit (actor_id, created_at DESC);

CREATE TRIGGER operator_audit_no_update BEFORE UPDATE ON operator_audit
BEGIN
    SELECT RAISE(ABORT, 'operator audit is append-only: UPDATE is not permitted');
END;

CREATE TRIGGER operator_audit_no_delete BEFORE DELETE ON operator_audit
BEGIN
    SELECT RAISE(ABORT, 'operator audit is append-only: DELETE is not permitted');
END;
