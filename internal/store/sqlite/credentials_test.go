package sqlite

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/vault"
)

func TestRotateCredentialsIsTransactional(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()
	for _, provider := range []string{"anthropic", "openai"} {
		if err := st.PutCredential(ctx, vault.Credential{
			Provider: provider, Nonce: []byte("old-nonce-12"), Ciphertext: []byte("old-" + provider),
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("PutCredential(%s): %v", provider, err)
		}
	}

	transformErr := errors.New("cannot decrypt anthropic")
	if _, err := st.RotateCredentials(ctx, func(c vault.Credential) (vault.Credential, error) {
		if c.Provider == "anthropic" {
			return vault.Credential{}, transformErr
		}
		c.Ciphertext = []byte("new-openai")
		return c, nil
	}); !errors.Is(err, transformErr) {
		t.Fatalf("RotateCredentials error = %v", err)
	}
	openAI, _ := st.GetCredential(ctx, "openai")
	if !bytes.Equal(openAI.Ciphertext, []byte("old-openai")) {
		t.Fatal("failed rotation partially committed another credential")
	}

	count, err := st.RotateCredentials(ctx, func(c vault.Credential) (vault.Credential, error) {
		c.Nonce = []byte("new-nonce-12")
		c.Ciphertext = append([]byte("new-"), c.Ciphertext...)
		c.UpdatedAt = now.Add(time.Minute)
		return c, nil
	})
	if err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	openAI, _ = st.GetCredential(ctx, "openai")
	if !bytes.Equal(openAI.Ciphertext, []byte("new-old-openai")) {
		t.Fatalf("rotated ciphertext = %q", openAI.Ciphertext)
	}
}
