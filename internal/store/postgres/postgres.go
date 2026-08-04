// Package postgres implements store.Store on PostgreSQL for multi-instance
// production deployments.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
	"github.com/premhiru/spendlease/internal/vault"
)

const driverName = "spendlease-postgres-pgx"

var registerDriver sync.Once

// Options configures the PostgreSQL connection pool.
type Options struct {
	Logger          *slog.Logger
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Store is a PostgreSQL-backed store.Store.
type Store struct{ *sqlite.Store }

// Open connects to PostgreSQL, applies serialized migrations, and returns a
// ready multi-instance store.
func Open(ctx context.Context, dsn string, opts Options) (*Store, error) {
	if err := validateDSN(dsn); err != nil {
		return nil, err
	}
	registerDriver.Do(func() {
		sql.Register(driverName, rebindingDriver{base: stdlib.GetDefaultDriver()})
	})

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening PostgreSQL database: %w", err)
	}
	maxOpen := opts.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 20
	}
	maxIdle := opts.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = maxOpen
	}
	lifetime := opts.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = 30 * time.Minute
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(lifetime)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to PostgreSQL database: %w", err)
	}
	if err := migrate(ctx, db, opts.Logger); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating PostgreSQL database: %w", err)
	}
	return &Store{Store: sqlite.AdoptPostgres(db, sqlite.Options{Logger: opts.Logger})}, nil
}

func validateDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("invalid PostgreSQL DSN: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "postgres" && scheme != "postgresql" {
		return fmt.Errorf("invalid PostgreSQL DSN: scheme must be postgres or postgresql")
	}
	if u.Host == "" {
		return fmt.Errorf("invalid PostgreSQL DSN: host is required")
	}
	return nil
}

var (
	_ store.Store           = (*Store)(nil)
	_ vault.CredentialStore = (*Store)(nil)
)
