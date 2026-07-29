package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/premhiru/spendlease/internal/vault"
)

// Environment variables read by spendlease.
const (
	// EnvMasterKey holds the 64-character hex master key that vendor
	// credentials are encrypted under.
	EnvMasterKey = "SPENDLEASE_MASTER_KEY"
	// EnvEnv marks the deployment environment. Setting it to "production"
	// refuses the development conveniences below.
	EnvEnv = "SPENDLEASE_ENV"
	// EnvAdminToken is the credential required to reach the dashboard and the
	// admin API from anywhere other than the local machine.
	EnvAdminToken = "SPENDLEASE_ADMIN_TOKEN"
)

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
//  1. SPENDLEASE_MASTER_KEY, if set.
//  2. A key file beside the database, if it exists.
//  3. A freshly generated key, persisted to that file.
//
// Step 3 is what makes `docker run` work with no configuration, and it is
// exactly what must not happen in production: a key generated and stored next
// to the data it protects is a convenience, not a secret. Setting
// SPENDLEASE_ENV=production refuses steps 2 and 3, so a misconfigured
// deployment fails to start rather than quietly encrypting credentials under
// a key sitting beside them.
func resolveMasterKey(storePath string) (key vault.MasterKey, source string, err error) {
	if raw := strings.TrimSpace(os.Getenv(EnvMasterKey)); raw != "" {
		k, err := vault.ParseMasterKey(raw)
		if err != nil {
			return vault.MasterKey{}, "", fmt.Errorf(
				"%s is set but unusable: %w\n"+
					"It must be %d bytes as %d hex characters. Generate one with:\n"+
					"  spendlease keys master generate",
				EnvMasterKey, err, vault.KeySize, vault.KeySize*2)
		}
		return k, "environment", nil
	}

	production := strings.EqualFold(os.Getenv(EnvEnv), "production")
	if production {
		return vault.MasterKey{}, "", fmt.Errorf(
			"%s is required when %s=production\n"+
				"Generate one with `spendlease keys master generate`, store it in your secret manager, "+
				"and provide it to the process.\n"+
				"Do not keep it beside the database: a key stored next to the data it protects is not a secret",
			EnvMasterKey, EnvEnv)
	}

	path := keyFilePath(storePath)

	switch b, readErr := os.ReadFile(path); {
	case readErr == nil:
		k, err := vault.ParseMasterKey(strings.TrimSpace(string(b)))
		if err != nil {
			return vault.MasterKey{}, "", fmt.Errorf("key file %s is unusable: %w", path, err)
		}
		return k, "key file " + path, nil
	case !errors.Is(readErr, fs.ErrNotExist):
		return vault.MasterKey{}, "", fmt.Errorf("reading key file %s: %w", path, readErr)
	}

	k, err := vault.GenerateMasterKey()
	if err != nil {
		return vault.MasterKey{}, "", err
	}
	if err := os.WriteFile(path, []byte(k.Hex()+"\n"), keyFilePerm); err != nil {
		return vault.MasterKey{}, "", fmt.Errorf("writing key file %s: %w", path, err)
	}
	return k, "newly generated, saved to " + path, nil
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
