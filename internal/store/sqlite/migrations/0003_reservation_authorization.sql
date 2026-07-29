-- Phase 7 reserves spend while requests still authenticate with principal
-- keys. Leases arrive in phase 8, so lease_id is optional until a real lease
-- authorized the request. See ADR-0015.
DROP INDEX reservations_pending_by_run;
DROP INDEX reservations_pending_by_expiry;

ALTER TABLE reservations RENAME TO reservations_old;

CREATE TABLE reservations (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL REFERENCES runs(id),
    lease_id     TEXT REFERENCES leases(id),
    amount_nanos INTEGER NOT NULL CHECK (amount_nanos >= 0),
    status       TEXT NOT NULL CHECK (status IN ('pending', 'settled', 'released', 'expired')),
    expires_at   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    resolved_at  TEXT,
    CHECK ((status = 'pending') = (resolved_at IS NULL))
) STRICT;

INSERT INTO reservations
    (id, run_id, lease_id, amount_nanos, status, expires_at, created_at, resolved_at)
SELECT id, run_id, lease_id, amount_nanos, status, expires_at, created_at, resolved_at
FROM reservations_old;

DROP TABLE reservations_old;

CREATE INDEX reservations_pending_by_run
    ON reservations (run_id) WHERE status = 'pending';
CREATE INDEX reservations_pending_by_expiry
    ON reservations (expires_at) WHERE status = 'pending';

-- A unique link makes settlement retry-safe without changing the immutable
-- ledger row or its hash format.
CREATE TABLE reservation_settlements (
    reservation_id TEXT PRIMARY KEY REFERENCES reservations(id),
    ledger_seq      INTEGER NOT NULL UNIQUE REFERENCES ledger(seq),
    created_at      TEXT NOT NULL
) STRICT;
