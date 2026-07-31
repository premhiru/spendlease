package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/store"
)

// PrincipalSummaries returns one row per principal with its totals, ordered by
// spend descending.
//
// The ordering is the whole point of the dashboard: the agent costing the most
// money is the one worth looking at, so it is the one at the top. Sorting
// happens in SQL rather than in Go because the ledger is the only place that
// knows the answer and it is already indexed by principal.
//
// Principals with no spend still appear, at the bottom. A registered agent
// that has never called anything is information too — it usually means an
// integration that is not working.
func (s *Store) PrincipalSummaries(ctx context.Context) ([]store.PrincipalSummary, error) {
	const q = `
		SELECT
			p.id,
			p.name,
			p.mode,
			p.created_at,
			COALESCE(l.spend, 0)      AS spend,
			COALESCE(l.entries, 0)    AS entries,
			COALESCE(l.estimated, 0)  AS estimated,
			COALESCE(r.runs, 0)       AS runs,
			COALESCE(ob.over, 0)      AS over_budget_runs,
			COALESCE(ls.active, 0)    AS active_leases,
			COALESCE(ls.revoked, 0)   AS revoked_leases,
			COALESCE(ls.expired, 0)   AS expired_leases,
			COALESCE(be.blocked, 0)   AS budget_blocks,
			COALESCE(be.observed, 0)  AS would_block_events,
			ls.last_revoked_at,
			ls.last_expired_at,
			be.last_at,
			l.last_at
		FROM principals p
		LEFT JOIN (
			SELECT principal_id,
			       SUM(cost_nanos)  AS spend,
			       COUNT(*)         AS entries,
			       SUM(estimated)   AS estimated,
			       MAX(created_at)  AS last_at
			FROM ledger
			GROUP BY principal_id
		) l ON l.principal_id = p.id
		LEFT JOIN (
			SELECT principal_id, COUNT(*) AS runs
			FROM runs
			GROUP BY principal_id
		) r ON r.principal_id = p.id
		LEFT JOIN (
			SELECT rr.principal_id,
			       SUM(CASE WHEN le.revoked_at IS NULL AND le.expires_at > ? THEN 1 ELSE 0 END) AS active,
			       SUM(CASE WHEN le.revoked_at IS NOT NULL THEN 1 ELSE 0 END) AS revoked,
			       SUM(CASE WHEN le.revoked_at IS NULL AND le.expires_at <= ? THEN 1 ELSE 0 END) AS expired,
			       MAX(le.revoked_at) AS last_revoked_at,
			       MAX(CASE WHEN le.revoked_at IS NULL AND le.expires_at <= ? THEN le.expires_at END) AS last_expired_at
			FROM leases le
			JOIN runs rr ON rr.id = le.run_id
			GROUP BY rr.principal_id
		) ls ON ls.principal_id = p.id
		LEFT JOIN (
			SELECT principal_id,
			       SUM(enforced) AS blocked,
			       SUM(CASE WHEN enforced = 0 THEN 1 ELSE 0 END) AS observed,
			       MAX(created_at) AS last_at
			FROM budget_events
			GROUP BY principal_id
		) be ON be.principal_id = p.id
		-- Historical observe-mode runs that actually spent past their budget.
		-- A zero budget means unset rather than "no allowance".
		LEFT JOIN (
			SELECT rr.principal_id, COUNT(*) AS over
			FROM runs rr
			JOIN (
				SELECT run_id, SUM(cost_nanos) AS spent
				FROM ledger
				GROUP BY run_id
			) rl ON rl.run_id = rr.id
			WHERE rr.budget_nanos > 0 AND rl.spent > rr.budget_nanos
			GROUP BY rr.principal_id
		) ob ON ob.principal_id = p.id
		ORDER BY spend DESC, p.created_at, p.id`

	now := formatTime(time.Now())
	rows, err := s.db.QueryContext(ctx, q, now, now, now)
	if err != nil {
		return nil, wrap(err, "summarising principals")
	}
	defer func() { _ = rows.Close() }()

	var out []store.PrincipalSummary
	for rows.Next() {
		var (
			sum                                          store.PrincipalSummary
			mode                                         string
			created                                      string
			spend                                        int64
			lastAt, lastRevoked, lastExpired, lastBudget sql.NullString
		)
		if err := rows.Scan(
			&sum.ID, &sum.Name, &mode, &created,
			&spend, &sum.Entries, &sum.EstimatedEntries, &sum.Runs,
			&sum.OverBudgetRuns,
			&sum.ActiveLeases, &sum.RevokedLeases, &sum.ExpiredLeases,
			&sum.BudgetBlocks, &sum.WouldBlockEvents,
			&lastRevoked, &lastExpired, &lastBudget, &lastAt,
		); err != nil {
			return nil, wrap(err, "scanning principal summary")
		}

		sum.Mode = store.Mode(mode)
		sum.Spend = money.Nanos(spend)

		var err error
		if sum.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if sum.LastActivity, err = parseNullTime(lastAt); err != nil {
			return nil, err
		}
		for _, raw := range []sql.NullString{lastAt, lastRevoked, lastExpired, lastBudget} {
			at, parseErr := parseNullTime(raw)
			if parseErr != nil {
				return nil, parseErr
			}
			if at != nil && (sum.LastEvent == nil || at.After(*sum.LastEvent)) {
				sum.LastEvent = at
			}
		}

		out = append(out, sum)
	}
	return out, wrap(rows.Err(), "iterating principal summaries")
}

// RunSummaries returns one row per run for a principal, ordered by spend
// descending.
//
// This is what a row expands into when somebody asks "which execution?" after
// seeing a principal's total.
func (s *Store) RunSummaries(ctx context.Context, principalID string) ([]store.RunSummary, error) {
	const q = `
		SELECT
			r.id,
			r.parent_run_id,
			r.budget_nanos,
			r.status,
			r.created_at,
			COALESCE(l.spend, 0)   AS spend,
			COALESCE(l.entries, 0) AS entries,
			l.last_at
		FROM runs r
		LEFT JOIN (
			SELECT run_id,
			       SUM(cost_nanos) AS spend,
			       COUNT(*)        AS entries,
			       MAX(created_at) AS last_at
			FROM ledger
			GROUP BY run_id
		) l ON l.run_id = r.id
		WHERE r.principal_id = ?
		ORDER BY spend DESC, r.created_at, r.id`

	rows, err := s.db.QueryContext(ctx, q, principalID)
	if err != nil {
		return nil, wrap(err, "summarising runs")
	}
	defer func() { _ = rows.Close() }()

	var out []store.RunSummary
	for rows.Next() {
		var (
			sum            store.RunSummary
			parent, lastAt sql.NullString
			status         string
			created        string
			budget, spend  int64
		)
		if err := rows.Scan(
			&sum.ID, &parent, &budget, &status, &created,
			&spend, &sum.Entries, &lastAt,
		); err != nil {
			return nil, wrap(err, "scanning run summary")
		}

		sum.PrincipalID = principalID
		sum.ParentRunID = parent.String
		sum.Budget = money.Nanos(budget)
		sum.Spend = money.Nanos(spend)
		sum.Status = store.RunStatus(status)

		var err error
		if sum.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if sum.LastActivity, err = parseNullTime(lastAt); err != nil {
			return nil, err
		}

		out = append(out, sum)
	}
	return out, wrap(rows.Err(), "iterating run summaries")
}
