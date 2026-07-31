-- Durable pre-egress budget decisions for the operational dashboard.
--
-- Successful requests already have the append-only ledger. A rejected
-- request deliberately has no ledger entry because it incurred no spend, but
-- losing the decision entirely makes enforcement invisible to an operator.

CREATE TABLE budget_events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    principal_id    TEXT NOT NULL REFERENCES principals(id),
    run_id           TEXT NOT NULL REFERENCES runs(id),
    lease_id         TEXT REFERENCES leases(id),
    provider         TEXT NOT NULL,
    model            TEXT NOT NULL,
    enforced         INTEGER NOT NULL CHECK (enforced IN (0, 1)),
    requested_nanos  INTEGER NOT NULL CHECK (requested_nanos >= 0),
    remaining_nanos  INTEGER NOT NULL CHECK (remaining_nanos >= 0),
    shortfall_nanos  INTEGER NOT NULL CHECK (shortfall_nanos >= 0),
    created_at       TEXT NOT NULL
) STRICT;

CREATE INDEX budget_events_by_principal
    ON budget_events (principal_id, created_at DESC);
CREATE INDEX budget_events_recent
    ON budget_events (created_at DESC);
