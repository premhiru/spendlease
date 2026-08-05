CREATE TABLE operators (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    token_hash TEXT NOT NULL UNIQUE,
    role TEXT NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    disabled_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE operator_audit (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_name TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    phase TEXT NOT NULL CHECK (phase IN ('attempt', 'result')),
    action TEXT NOT NULL,
    resource TEXT NOT NULL,
    remote_addr TEXT NOT NULL,
    status_code INTEGER NOT NULL CHECK (status_code >= 0),
    created_at TEXT NOT NULL
);
CREATE INDEX operator_audit_recent ON operator_audit (created_at DESC, id);
CREATE INDEX operator_audit_actor ON operator_audit (actor_id, created_at DESC);

CREATE FUNCTION reject_operator_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'operator audit is append-only: % is not permitted', TG_OP
        USING ERRCODE = '55000';
END;
$$;
CREATE TRIGGER operator_audit_no_update BEFORE UPDATE ON operator_audit
FOR EACH ROW EXECUTE FUNCTION reject_operator_audit_mutation();
CREATE TRIGGER operator_audit_no_delete BEFORE DELETE ON operator_audit
FOR EACH ROW EXECUTE FUNCTION reject_operator_audit_mutation();
