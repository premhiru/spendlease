package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/operator"
	"github.com/premhiru/spendlease/internal/store"
)

// CreateOperator stores a named identity with a hashed bearer token.
func (s *Store) CreateOperator(ctx context.Context, op operator.Operator) error {
	if strings.TrimSpace(op.Name) == "" || !op.Role.Valid() || op.TokenHash == "" {
		return fmt.Errorf("%w: operator name, role, and token hash are required", store.ErrConflict)
	}
	now := op.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if op.UpdatedAt.IsZero() {
		op.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO operators (id, name, token_hash, role, disabled_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		op.ID, strings.TrimSpace(op.Name), op.TokenHash, string(op.Role), nullTime(op.DisabledAt),
		formatTime(now), formatTime(op.UpdatedAt))
	return wrap(err, "creating operator")
}

// GetOperatorByTokenHash resolves the authentication digest.
func (s *Store) GetOperatorByTokenHash(ctx context.Context, hash string) (operator.Operator, error) {
	return scanOperator(s.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, role, disabled_at, created_at, updated_at
		 FROM operators WHERE token_hash = ?`, hash))
}

// HasActiveOperators reports whether named remote access is configured.
func (s *Store) HasActiveOperators(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operators WHERE disabled_at IS NULL`).Scan(&count); err != nil {
		return false, wrap(err, "counting active operators")
	}
	return count > 0, nil
}

// ListOperators returns every identity, including revoked identities.
func (s *Store) ListOperators(ctx context.Context) ([]operator.Operator, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, token_hash, role, disabled_at, created_at, updated_at
		 FROM operators ORDER BY created_at, id`)
	if err != nil {
		return nil, wrap(err, "listing operators")
	}
	defer func() { _ = rows.Close() }()
	var out []operator.Operator
	for rows.Next() {
		op, err := scanOperator(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, wrap(rows.Err(), "iterating operators")
}

func scanOperator(row scanner) (operator.Operator, error) {
	var op operator.Operator
	var role, created, updated string
	var disabled sql.NullString
	if err := row.Scan(&op.ID, &op.Name, &op.TokenHash, &role, &disabled, &created, &updated); err != nil {
		return operator.Operator{}, wrap(err, "reading operator")
	}
	op.Role = operator.Role(role)
	var err error
	if op.CreatedAt, err = parseTime(created); err != nil {
		return operator.Operator{}, err
	}
	if op.UpdatedAt, err = parseTime(updated); err != nil {
		return operator.Operator{}, err
	}
	if op.DisabledAt, err = parseNullTime(disabled); err != nil {
		return operator.Operator{}, err
	}
	return op, nil
}

// SetOperatorRole changes one identity's permission tier while preserving at
// least one active admin.
func (s *Store) SetOperatorRole(ctx context.Context, id string, role operator.Role, at time.Time) error {
	if !role.Valid() {
		return fmt.Errorf("%w: invalid operator role %q", store.ErrConflict, role)
	}
	return s.changeOperator(ctx, id, func(tx *sql.Tx, current operator.Operator) error {
		if current.Role == operator.RoleAdmin && role != operator.RoleAdmin && current.Active() {
			if err := requireAnotherAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE operators SET role = ?, updated_at = ? WHERE id = ?`,
			string(role), formatTime(at), id)
		if err != nil {
			return wrap(err, "setting operator role")
		}
		return requireAffected(res, "operator", id)
	})
}

// RotateOperatorToken atomically replaces an active identity's token digest.
func (s *Store) RotateOperatorToken(ctx context.Context, id, tokenHash string, at time.Time) error {
	if tokenHash == "" {
		return fmt.Errorf("%w: operator token hash is required", store.ErrConflict)
	}
	return s.changeOperator(ctx, id, func(tx *sql.Tx, current operator.Operator) error {
		if !current.Active() {
			return fmt.Errorf("%w: operator %s is disabled", store.ErrConflict, id)
		}
		res, err := tx.ExecContext(ctx, `UPDATE operators SET token_hash = ?, updated_at = ? WHERE id = ?`,
			tokenHash, formatTime(at), id)
		if err != nil {
			return wrap(err, "rotating operator token")
		}
		return requireAffected(res, "operator", id)
	})
}

// DisableOperator irreversibly rejects an identity's current token while
// preserving at least one active admin.
func (s *Store) DisableOperator(ctx context.Context, id string, at time.Time) error {
	return s.changeOperator(ctx, id, func(tx *sql.Tx, current operator.Operator) error {
		if !current.Active() {
			return nil
		}
		if current.Role == operator.RoleAdmin {
			if err := requireAnotherAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE operators SET disabled_at = ?, updated_at = ? WHERE id = ? AND disabled_at IS NULL`,
			formatTime(at), formatTime(at), id)
		if err != nil {
			return wrap(err, "disabling operator")
		}
		return requireAffected(res, "operator", id)
	})
}

func (s *Store) changeOperator(
	ctx context.Context,
	id string,
	change func(*sql.Tx, operator.Operator) error,
) error {
	s.operatorMu.Lock()
	defer s.operatorMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap(err, "beginning operator change")
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockOperatorsTx(ctx, tx); err != nil {
		return err
	}
	current, err := scanOperator(tx.QueryRowContext(ctx,
		`SELECT id, name, token_hash, role, disabled_at, created_at, updated_at
		 FROM operators WHERE id = ?`, id))
	if err != nil {
		return err
	}
	if err := change(tx, current); err != nil {
		return err
	}
	return wrap(tx.Commit(), "committing operator change")
}

func requireAnotherAdmin(ctx context.Context, tx *sql.Tx, excludingID string) error {
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM operators WHERE role = 'admin' AND disabled_at IS NULL AND id != ?`,
		excludingID).Scan(&count); err != nil {
		return wrap(err, "counting active admins")
	}
	if count == 0 {
		return fmt.Errorf("%w: cannot remove the final active admin", store.ErrConflict)
	}
	return nil
}

// AppendOperatorAudit adds one immutable mutation event.
func (s *Store) AppendOperatorAudit(ctx context.Context, record operator.AuditRecord) error {
	if !record.Role.Valid() || record.ID == "" || record.RequestID == "" || record.Action == "" {
		return fmt.Errorf("%w: incomplete operator audit record", store.ErrConflict)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO operator_audit
		 (id, request_id, actor_id, actor_name, role, phase, action, resource, remote_addr, status_code, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.RequestID, record.ActorID, record.ActorName, string(record.Role), record.Phase,
		record.Action, record.Resource, record.RemoteAddr, record.StatusCode, formatTime(record.CreatedAt))
	return wrap(err, "appending operator audit")
}

// ListOperatorAudit returns newest matching control events.
func (s *Store) ListOperatorAudit(ctx context.Context, filter operator.AuditFilter) ([]operator.AuditRecord, error) {
	query := `SELECT id, request_id, actor_id, actor_name, role, phase, action, resource,
		 remote_addr, status_code, created_at FROM operator_audit WHERE 1 = 1`
	var args []any
	if filter.ActorID != "" {
		query += ` AND actor_id = ?`
		args = append(args, filter.ActorID)
	}
	if filter.Action != "" {
		query += ` AND action = ?`
		args = append(args, filter.Action)
	}
	if !filter.Since.IsZero() {
		query += ` AND created_at >= ?`
		args = append(args, formatTime(filter.Since))
	}
	query += ` ORDER BY created_at DESC, id DESC`
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query += ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap(err, "listing operator audit")
	}
	defer func() { _ = rows.Close() }()
	var out []operator.AuditRecord
	for rows.Next() {
		var record operator.AuditRecord
		var role, created string
		if err := rows.Scan(&record.ID, &record.RequestID, &record.ActorID, &record.ActorName, &role,
			&record.Phase, &record.Action, &record.Resource, &record.RemoteAddr, &record.StatusCode, &created); err != nil {
			return nil, wrap(err, "scanning operator audit")
		}
		record.Role = operator.Role(role)
		if record.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, wrap(rows.Err(), "iterating operator audit")
}
