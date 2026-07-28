package store

import (
	"strings"
	"testing"
)

func TestNewIDShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		gen    func() string
		prefix string
	}{
		{"principal", NewPrincipalID, PrincipalPrefix},
		{"run", NewRunID, RunPrefix},
		{"lease", NewLeaseID, LeasePrefix},
		{"reservation", NewReservationID, ReservationPrefix},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id := tt.gen()
			if !strings.HasPrefix(id, tt.prefix) {
				t.Errorf("id %q does not start with %q", id, tt.prefix)
			}

			body := strings.TrimPrefix(id, tt.prefix)
			if body == "" {
				t.Fatal("id has no body after the prefix")
			}
			for _, r := range body {
				isLower := r >= 'a' && r <= 'z'
				isBase32Digit := r >= '2' && r <= '7'
				if !isLower && !isBase32Digit {
					t.Errorf("id %q contains %q, which is not in the base32 alphabet", id, r)
				}
			}
			if strings.ContainsAny(id, "=/+") {
				t.Errorf("id %q contains characters needing escaping", id)
			}
		})
	}
}

func TestNewIDIsUnique(t *testing.T) {
	t.Parallel()

	const n = 10_000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := NewRunID()
		if seen[id] {
			t.Fatalf("NewRunID produced a duplicate after %d draws: %s", i, id)
		}
		seen[id] = true
	}
}

func TestNewSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		gen    func() (string, string)
		prefix string
		looks  func(string) bool
	}{
		{"principal key", NewPrincipalKey, PrincipalKeyPrefix, LooksLikePrincipalKey},
		{"lease token", NewLeaseToken, LeaseTokenPrefix, LooksLikeLeaseToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plaintext, hash := tt.gen()

			if !strings.HasPrefix(plaintext, tt.prefix) {
				t.Errorf("secret %q does not start with %q", plaintext, tt.prefix)
			}
			if !tt.looks(plaintext) {
				t.Error("the shape check does not recognise its own output")
			}
			if len(hash) != 64 {
				t.Errorf("hash length = %d, want 64 hex characters", len(hash))
			}
			if strings.Contains(hash, plaintext) {
				t.Fatal("the hash contains the plaintext")
			}
			if hash != HashSecret(plaintext) {
				t.Error("the returned hash does not match HashSecret of the plaintext")
			}

			// 32 random bytes in base32 is 52 characters.
			if body := strings.TrimPrefix(plaintext, tt.prefix); len(body) != 52 {
				t.Errorf("secret body length = %d, want 52", len(body))
			}
		})
	}
}

func TestSecretsAreUnique(t *testing.T) {
	t.Parallel()

	const n = 1_000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		s, _ := NewLeaseToken()
		if seen[s] {
			t.Fatalf("NewLeaseToken produced a duplicate after %d draws", i)
		}
		seen[s] = true
	}
}

func TestSecretMatches(t *testing.T) {
	t.Parallel()

	plaintext, hash := NewPrincipalKey()
	other, _ := NewPrincipalKey()

	tests := []struct {
		name      string
		presented string
		stored    string
		want      bool
	}{
		{"correct secret", plaintext, hash, true},
		{"different secret", other, hash, false},
		{"empty presented", "", hash, false},
		{"empty stored", plaintext, "", false},
		{"truncated secret", plaintext[:len(plaintext)-1], hash, false},
		{"hash presented as the secret", hash, hash, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := SecretMatches(tt.presented, tt.stored); got != tt.want {
				t.Errorf("SecretMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHashSecretIsStable(t *testing.T) {
	t.Parallel()

	// A fixed vector computed independently with sha256sum. If the hashing
	// scheme is ever changed, this fails loudly — which matters, because a
	// silent change would invalidate every principal key and lease token
	// already stored, with no way to recover them.
	const input = "slk_example"
	const want = "83f1e669813eadac3a95e71bc80a5eef65ae5cf89eac7775e4620f3588bca1b7"

	got := HashSecret(input)
	if got != want {
		t.Errorf("HashSecret(%q) = %q, want %q", input, got, want)
	}
	if HashSecret(input+"x") == got {
		t.Error("different inputs hashed identically")
	}
}

func TestLooksLikeGuardsRejectWrongPrefix(t *testing.T) {
	t.Parallel()

	if LooksLikePrincipalKey("sll_something") {
		t.Error("a lease token was accepted as a principal key")
	}
	if LooksLikeLeaseToken("slk_something") {
		t.Error("a principal key was accepted as a lease token")
	}
	if LooksLikePrincipalKey("") || LooksLikeLeaseToken("") {
		t.Error("an empty string was accepted")
	}
}

func TestModeAndStatusValidation(t *testing.T) {
	t.Parallel()

	if !ModeObserve.Valid() || !ModeEnforce.Valid() {
		t.Error("a documented mode reported invalid")
	}
	if Mode("audit").Valid() || Mode("").Valid() {
		t.Error("an unknown mode reported valid")
	}

	if !RunActive.Valid() || !RunClosed.Valid() {
		t.Error("a documented run status reported invalid")
	}
	if RunStatus("paused").Valid() {
		t.Error("an unknown run status reported valid")
	}

	for _, s := range []ReservationStatus{
		ReservationPending, ReservationSettled, ReservationReleased, ReservationExpired,
	} {
		if !s.Valid() {
			t.Errorf("reservation status %q reported invalid", s)
		}
	}
	if ReservationStatus("void").Valid() {
		t.Error("an unknown reservation status reported valid")
	}
}

func TestLeaseProviderScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		providers []string
		probe     string
		want      bool
	}{
		{"empty scope allows everything", nil, "openai", true},
		{"empty slice allows everything", []string{}, "anthropic", true},
		{"listed provider is allowed", []string{"openai"}, "openai", true},
		{"unlisted provider is denied", []string{"openai"}, "anthropic", false},
		{"one of several", []string{"openai", "anthropic"}, "anthropic", true},
		{"unknown provider is denied", []string{"openai"}, "cohere", false},
		{"matching is exact", []string{"openai"}, "openai-beta", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := Lease{Providers: tt.providers}
			if got := l.AllowsProvider(tt.probe); got != tt.want {
				t.Errorf("AllowsProvider(%q) = %v, want %v", tt.probe, got, tt.want)
			}
		})
	}
}
