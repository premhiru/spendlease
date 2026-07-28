package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/vault"
)

// Credential persistence lives on the same store as everything else, but is
// kept off the store.Store interface on purpose: it is the vault's concern,
// and the vault declares the narrow interface it needs. The assertion at the
// bottom of this file is what keeps a backend honest about implementing it.

// PutCredential inserts or replaces a provider's encrypted vendor key.
//
// Unlike the ledger, credentials are meant to be replaced: rotating a vendor
// key is a normal operation. The original creation time is preserved across
// rotations so "when was this first configured" survives.
func (s *Store) PutCredential(ctx context.Context, c vault.Credential) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO credentials (provider, nonce, ciphertext, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(provider) DO UPDATE SET
		     nonce      = excluded.nonce,
		     ciphertext = excluded.ciphertext,
		     updated_at = excluded.updated_at`,
		c.Provider, c.Nonce, c.Ciphertext, formatTime(c.CreatedAt), formatTime(c.UpdatedAt),
	)
	return wrap(err, "storing credential")
}

// GetCredential returns a provider's encrypted vendor key.
func (s *Store) GetCredential(ctx context.Context, provider string) (vault.Credential, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT provider, nonce, ciphertext, created_at, updated_at
		 FROM credentials WHERE provider = ?`, provider)

	var c vault.Credential
	var created, updated string
	if err := row.Scan(&c.Provider, &c.Nonce, &c.Ciphertext, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return vault.Credential{}, fmt.Errorf("%w: %s", vault.ErrNoCredential, provider)
		}
		return vault.Credential{}, wrap(err, "reading credential")
	}

	var err error
	if c.CreatedAt, err = parseTime(created); err != nil {
		return vault.Credential{}, err
	}
	if c.UpdatedAt, err = parseTime(updated); err != nil {
		return vault.Credential{}, err
	}
	return c, nil
}

// ListCredentialProviders returns the providers that have a stored key, in
// alphabetical order. It returns names only; no ciphertext leaves the store.
func (s *Store) ListCredentialProviders(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider FROM credentials ORDER BY provider`)
	if err != nil {
		return nil, wrap(err, "listing credentials")
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, wrap(err, "scanning credential provider")
		}
		out = append(out, p)
	}
	return out, wrap(rows.Err(), "iterating credentials")
}

// DeleteCredential removes a provider's stored key. Deleting one that does
// not exist returns ErrNotFound rather than succeeding quietly, so an
// operator who mistypes a provider name finds out.
func (s *Store) DeleteCredential(ctx context.Context, provider string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE provider = ?`, provider)
	if err != nil {
		return wrap(err, "deleting credential")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrap(err, "deleting credential")
	}
	if n == 0 {
		return fmt.Errorf("%w: credential %s", store.ErrNotFound, provider)
	}
	return nil
}

// Verify compiles only if *Store can back a vault.
var _ vault.CredentialStore = (*Store)(nil)
