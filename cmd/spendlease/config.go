package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/premhiru/spendlease/internal/keysource"
	"github.com/premhiru/spendlease/internal/vault"
)

// Environment variables read by spendlease.
const (
	// EnvMasterKey holds the 64-character hex master key that vendor
	// credentials are encrypted under.
	EnvMasterKey = "SPENDLEASE_MASTER_KEY"
	// EnvMasterKeyFile reads the primary key from a mounted secret file.
	EnvMasterKeyFile = "SPENDLEASE_MASTER_KEY_FILE"
	// EnvMasterKeyCommand is a JSON argv array whose stdout is the primary key.
	EnvMasterKeyCommand = "SPENDLEASE_MASTER_KEY_COMMAND"
	// Previous-key sources are temporary fallbacks during staged rotation.
	EnvPreviousMasterKey        = "SPENDLEASE_PREVIOUS_MASTER_KEY"
	EnvPreviousMasterKeyFile    = "SPENDLEASE_PREVIOUS_MASTER_KEY_FILE"
	EnvPreviousMasterKeyCommand = "SPENDLEASE_PREVIOUS_MASTER_KEY_COMMAND"
	// EnvEnv marks the deployment environment. Setting it to "production"
	// refuses the development conveniences below.
	EnvEnv = "SPENDLEASE_ENV"
	// EnvAdminToken is the credential required to reach the dashboard and the
	// admin API from anywhere other than the local machine.
	EnvAdminToken = "SPENDLEASE_ADMIN_TOKEN"
	// EnvAlertWebhook is the optional operational alert destination.
	EnvAlertWebhook = "SPENDLEASE_ALERT_WEBHOOK"
	// EnvAlertWebhookSecret signs alert bodies with HMAC-SHA256.
	EnvAlertWebhookSecret = "SPENDLEASE_ALERT_WEBHOOK_SECRET"
	// EnvStore selects a SQLite path or PostgreSQL DSN. The --store flag uses
	// this as its default and can still override it.
	EnvStore = "SPENDLEASE_STORE"
)

func defaultStore() string {
	if target := strings.TrimSpace(os.Getenv(EnvStore)); target != "" {
		return target
	}
	return "./spendlease.db"
}

// resolveAdminToken returns the admin credential, from the flag if given and
// the environment otherwise.
//
// There is deliberately no generated fallback, unlike the master key. A
// generated master key still encrypts the credentials it protects, so it is
// worth something. A generated admin token that nobody has read protects
// nothing and would only create the impression of a control: the operator
// would not know it, so they could not use it, and access would be refused
// anyway. Refusing outright says so plainly instead.
func resolveAdminToken(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(EnvAdminToken))
}

// keyFilePerm is owner read/write only. The master key decrypts every stored
// vendor credential, so it is treated like an SSH private key.
const keyFilePerm fs.FileMode = 0o600

// resolveMasterKey determines the master key for this process.
//
// In order of precedence:
//
//  1. One explicit environment, mounted-file, or command source.
//  2. A development key file beside a SQLite database, if it exists.
//  3. A freshly generated development key, persisted to that file.
//
// Step 3 is what makes `docker run` work with no configuration, and it is
// exactly what must not happen in production: a key generated and stored next
// to the data it protects is a convenience, not a secret. Setting
// SPENDLEASE_ENV=production refuses steps 2 and 3, so a misconfigured
// deployment fails to start rather than quietly encrypting credentials under
// a key sitting beside them.
type masterKeys struct {
	Primary         vault.MasterKey
	Previous        []vault.MasterKey
	ExplicitPrimary bool
}

func resolveMasterKey(storePath string) (key vault.MasterKey, source string, err error) {
	keys, source, err := resolveMasterKeys(context.Background(), storePath)
	return keys.Primary, source, err
}

func resolveMasterKeys(ctx context.Context, storePath string) (keys masterKeys, source string, err error) {
	resolver := keysource.Resolver{}
	primary, err := resolver.Resolve(ctx, keysource.Spec{
		ValueEnv: EnvMasterKey, FileEnv: EnvMasterKeyFile, CommandEnv: EnvMasterKeyCommand,
		Label: "master key",
	})
	if err != nil {
		return masterKeys{}, "", masterKeySourceError(err)
	}
	previous, err := resolver.Resolve(ctx, keysource.Spec{
		ValueEnv: EnvPreviousMasterKey, FileEnv: EnvPreviousMasterKeyFile, CommandEnv: EnvPreviousMasterKeyCommand,
		Label: "previous master key",
	})
	if err != nil {
		return masterKeys{}, "", masterKeySourceError(err)
	}
	if primary.Present {
		keys.Primary = primary.Key
		keys.ExplicitPrimary = true
		source = primary.Source
	}

	production := strings.EqualFold(os.Getenv(EnvEnv), "production")
	if !primary.Present && (production || isPostgresDSN(storePath)) {
		return masterKeys{}, "", fmt.Errorf(
			"a master key source is required for production or PostgreSQL storage\n"+
				"Set exactly one of %s, %s, or %s.\n"+
				"Generate one with `spendlease keys master generate`, store it in your secret manager, "+
				"and provide it to the process.\n"+
				"Do not keep it beside the database: a key stored next to the data it protects is not a secret",
			EnvMasterKey, EnvMasterKeyFile, EnvMasterKeyCommand)
	}

	if !primary.Present {
		path := keyFilePath(storePath)

		switch b, readErr := os.ReadFile(path); {
		case readErr == nil:
			k, err := vault.ParseMasterKey(strings.TrimSpace(string(b)))
			if err != nil {
				return masterKeys{}, "", fmt.Errorf("key file %s is unusable: %w", path, err)
			}
			keys.Primary = k
			source = "key file " + path
		case !errors.Is(readErr, fs.ErrNotExist):
			return masterKeys{}, "", fmt.Errorf("reading key file %s: %w", path, readErr)
		default:
			k, err := vault.GenerateMasterKey()
			if err != nil {
				return masterKeys{}, "", err
			}
			if err := os.WriteFile(path, []byte(k.Hex()+"\n"), keyFilePerm); err != nil {
				return masterKeys{}, "", fmt.Errorf("writing key file %s: %w", path, err)
			}
			keys.Primary = k
			source = "newly generated, saved to " + path
		}
	}

	if previous.Present {
		if previous.Key == keys.Primary {
			return masterKeys{}, "", errors.New("previous master key must differ from the primary master key")
		}
		keys.Previous = []vault.MasterKey{previous.Key}
		source += "; previous from " + previous.Source
	}
	return keys, source, nil
}

func masterKeySourceError(err error) error {
	if !errors.Is(err, vault.ErrBadMasterKey) {
		return err
	}
	return fmt.Errorf("%w\nThe resolved value must be %d bytes as %d hex characters. Generate one with:\n  spendlease keys master generate",
		err, vault.KeySize, vault.KeySize*2)
}

// keyFilePath returns where the development key file lives for a given store
// path: alongside it, with a .key extension.
func keyFilePath(storePath string) string {
	dir := filepath.Dir(storePath)
	base := strings.TrimSuffix(filepath.Base(storePath), filepath.Ext(storePath))
	if base == "" {
		base = "spendlease"
	}
	return filepath.Join(dir, base+".key")
}
