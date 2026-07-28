package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/store"
)

// timeLayout is RFC 3339 with nanosecond precision. Stored as TEXT in UTC, it
// sorts lexicographically in the same order it sorts chronologically, so
// range queries and ORDER BY work without any conversion.
const timeLayout = time.RFC3339Nano

// formatTime renders an instant for storage, always in UTC.
func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime reads a stored instant back.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: cannot parse stored timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

// nullTime converts an optional instant to a driver argument.
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// parseNullTime converts a nullable stored timestamp back to an optional
// instant.
func parseNullTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// nullString converts an empty string to SQL NULL, so that "no parent run"
// is a NULL the foreign key ignores rather than an empty string it rejects.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// boolToInt converts a bool for storage, since SQLite has no boolean type.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// encodeProviders flattens a lease's provider scope for storage. An empty
// scope means every provider and is stored as an empty string.
func encodeProviders(providers []string) string {
	return strings.Join(providers, ",")
}

// decodeProviders restores a lease's provider scope.
func decodeProviders(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// requireAffected turns "the UPDATE matched nothing" into ErrNotFound, so a
// write against a missing row is not mistaken for a successful no-op.
func requireAffected(res sql.Result, kind, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: checking affected rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s %s", store.ErrNotFound, kind, id)
	}
	return nil
}

// wrap translates a driver error into one of the store's sentinel errors, so
// callers never have to match on SQLite-specific text.
//
// The mapping is done on the message rather than the extended result code
// because those messages are stable across SQLite versions and are the same
// strings the pure-Go translation produces. The trigger case is matched on
// the text this schema's triggers raise, which this package controls.
func wrap(err error, doing string) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", store.ErrNotFound, doing)
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "append-only"):
		// Raised by the ledger_no_update and ledger_no_delete triggers.
		return fmt.Errorf("%w: %s: %v", store.ErrImmutable, doing, err)
	case strings.Contains(msg, "UNIQUE constraint failed"),
		strings.Contains(msg, "PRIMARY KEY constraint failed"):
		return fmt.Errorf("%w: %s: %v", store.ErrConflict, doing, err)
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		// The referenced principal, run or lease does not exist.
		return fmt.Errorf("%w: %s: %v", store.ErrNotFound, doing, err)
	case strings.Contains(msg, "CHECK constraint failed"):
		return fmt.Errorf("%w: %s: %v", store.ErrConflict, doing, err)
	}

	return fmt.Errorf("store: %s: %w", doing, err)
}
