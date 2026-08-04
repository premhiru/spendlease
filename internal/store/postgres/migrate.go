package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func migrate(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	all, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// All instances share this transaction-scoped lock, so only one startup
	// can inspect and advance the schema at a time.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('spendlease:migrations', 0))`); err != nil {
		return fmt.Errorf("locking migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("reading schema_migrations: %w", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scanning schema_migrations: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterating schema_migrations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("closing schema_migrations: %w", err)
	}

	for _, m := range all {
		if applied[m.version] {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("applying migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`,
			m.version, m.name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}
		logger.Info("applied PostgreSQL migration", "version", m.version, "name", m.name)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing migrations: %w", err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}
	var out []migration
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q is not named NNNN_description.sql", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", entry.Name(), err)
		}
		if prior, exists := seen[version]; exists {
			return nil, fmt.Errorf("migrations %q and %q share version %d", prior, entry.Name(), version)
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading migration %q: %w", entry.Name(), err)
		}
		seen[version] = entry.Name()
		out = append(out, migration{version: version, name: entry.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
