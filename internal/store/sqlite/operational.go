package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

// RecordBudgetEvent persists a reservation decision that did not fit. It is
// separate from the ledger because a rejected request incurred no spend.
func (s *Store) RecordBudgetEvent(ctx context.Context, e store.BudgetEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO budget_events
			(principal_id, run_id, lease_id, provider, model, enforced,
			 requested_nanos, remaining_nanos, shortfall_nanos, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.PrincipalID, e.RunID, nullString(e.LeaseID), e.Provider, e.Model,
		boolToInt(e.Enforced), int64(e.Requested), int64(e.Remaining),
		int64(e.Shortfall), formatTime(e.CreatedAt),
	)
	return wrap(err, "recording budget event")
}

// RecentOperationalEvents combines the immutable ledger, budget decisions and
// lease lifecycle into one newest-first operator timeline.
func (s *Store) RecentOperationalEvents(
	ctx context.Context,
	limit int,
	now time.Time,
) ([]store.OperationalEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	const q = `
		SELECT kind, principal_id, principal_name, run_id, lease_id,
		       provider, model, amount_nanos, remaining_nanos, event_at
		FROM (
			SELECT 'allowed' AS kind, l.principal_id, p.name AS principal_name,
			       l.run_id, '' AS lease_id, l.provider, l.model,
			       l.cost_nanos AS amount_nanos, 0 AS remaining_nanos,
			       l.created_at AS event_at, l.seq AS event_order
			FROM ledger l
			JOIN principals p ON p.id = l.principal_id

			UNION ALL

			SELECT CASE WHEN b.enforced = 1 THEN 'budget_blocked'
			            ELSE 'budget_would_block' END AS kind,
			       b.principal_id, p.name AS principal_name, b.run_id,
			       COALESCE(b.lease_id, '') AS lease_id, b.provider, b.model,
			       b.requested_nanos AS amount_nanos,
			       b.remaining_nanos, b.created_at AS event_at,
			       b.id AS event_order
			FROM budget_events b
			JOIN principals p ON p.id = b.principal_id

			UNION ALL

			SELECT 'lease_revoked' AS kind, p.id AS principal_id,
			       p.name AS principal_name, r.id AS run_id, le.id AS lease_id,
			       le.providers AS provider, '' AS model,
			       le.ceiling_nanos AS amount_nanos, 0 AS remaining_nanos,
			       le.revoked_at AS event_at, le.rowid AS event_order
			FROM leases le
			JOIN runs r ON r.id = le.run_id
			JOIN principals p ON p.id = r.principal_id
			WHERE le.revoked_at IS NOT NULL

			UNION ALL

			SELECT 'lease_expired' AS kind, p.id AS principal_id,
			       p.name AS principal_name, r.id AS run_id, le.id AS lease_id,
			       le.providers AS provider, '' AS model,
			       le.ceiling_nanos AS amount_nanos, 0 AS remaining_nanos,
			       le.expires_at AS event_at, le.rowid AS event_order
			FROM leases le
			JOIN runs r ON r.id = le.run_id
			JOIN principals p ON p.id = r.principal_id
			WHERE le.revoked_at IS NULL AND le.expires_at <= ?
		)
		ORDER BY event_at DESC, event_order DESC
		LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, formatTime(now), limit)
	if err != nil {
		return nil, wrap(err, "reading operational events")
	}
	defer func() { _ = rows.Close() }()

	var out []store.OperationalEvent
	for rows.Next() {
		var e store.OperationalEvent
		var kind, created string
		var amount, remaining int64
		if err := rows.Scan(
			&kind, &e.PrincipalID, &e.PrincipalName, &e.RunID, &e.LeaseID,
			&e.Provider, &e.Model, &amount, &remaining, &created,
		); err != nil {
			return nil, wrap(err, "scanning operational event")
		}
		e.Kind = store.OperationalEventKind(kind)
		e.Amount = money.Nanos(amount)
		e.Remaining = money.Nanos(remaining)
		if e.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if !validEventKind(e.Kind) {
			return nil, fmt.Errorf("store: unknown operational event kind %q", e.Kind)
		}
		out = append(out, e)
	}
	return out, wrap(rows.Err(), "iterating operational events")
}

func validEventKind(kind store.OperationalEventKind) bool {
	switch kind {
	case store.EventAllowed, store.EventBudgetBlocked, store.EventBudgetWouldBlock,
		store.EventLeaseRevoked, store.EventLeaseExpired:
		return true
	default:
		return false
	}
}
