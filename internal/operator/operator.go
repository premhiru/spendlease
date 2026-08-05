// Package operator defines the human identities that may use spendlease's
// control plane and the immutable audit records their mutations produce.
package operator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	idPrefix    = "opr_"
	tokenPrefix = "slo_"
	auditPrefix = "aud_"
)

// Role is one cumulative control-plane permission tier.
type Role string

const (
	// RoleViewer permits read-only dashboard and API access.
	RoleViewer Role = "viewer"
	// RoleOperator also permits run and lease lifecycle changes.
	RoleOperator Role = "operator"
	// RoleAdmin also permits enforcement, kill-switch, and audit access.
	RoleAdmin Role = "admin"
)

// Valid reports whether the role is recognized.
func (r Role) Valid() bool {
	return r == RoleViewer || r == RoleOperator || r == RoleAdmin
}

// Allows reports whether r includes every permission in required.
func (r Role) Allows(required Role) bool {
	return roleRank(r) >= roleRank(required)
}

func roleRank(role Role) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	default:
		return 0
	}
}

// Operator is one named control-plane identity. TokenHash is the only stored
// representation of its bearer credential.
type Operator struct {
	ID         string
	Name       string
	TokenHash  string
	Role       Role
	DisabledAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Active reports whether the operator credential may authenticate.
func (o Operator) Active() bool { return o.DisabledAt == nil }

// Identity is the authenticated subset attached to an HTTP request.
type Identity struct {
	ID   string
	Name string
	Role Role
}

// AuditRecord is one immutable attempt or result in the control audit trail.
type AuditRecord struct {
	ID         string    `json:"id"`
	RequestID  string    `json:"request_id"`
	ActorID    string    `json:"actor_id"`
	ActorName  string    `json:"actor_name"`
	Role       Role      `json:"role"`
	Phase      string    `json:"phase"`
	Action     string    `json:"action"`
	Resource   string    `json:"resource"`
	RemoteAddr string    `json:"remote_addr"`
	StatusCode int       `json:"status_code"`
	CreatedAt  time.Time `json:"created_at"`
}

// AuditFilter narrows a newest-first audit query.
type AuditFilter struct {
	ActorID string
	Action  string
	Since   time.Time
	Limit   int
}

// Store persists operator identities and their append-only control audit.
type Store interface {
	CreateOperator(context.Context, Operator) error
	GetOperatorByTokenHash(context.Context, string) (Operator, error)
	HasActiveOperators(context.Context) (bool, error)
	ListOperators(context.Context) ([]Operator, error)
	SetOperatorRole(context.Context, string, Role, time.Time) error
	RotateOperatorToken(context.Context, string, string, time.Time) error
	DisableOperator(context.Context, string, time.Time) error
	AppendOperatorAudit(context.Context, AuditRecord) error
	ListOperatorAudit(context.Context, AuditFilter) ([]AuditRecord, error)
}

var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("operator: cannot read random bytes: %v", err))
	}
	return prefix + encoding.EncodeToString(b)
}

// NewOperatorID returns a random opr_ identifier.
func NewOperatorID() string { return newID(idPrefix) }

// NewAuditID returns a random aud_ identifier.
func NewAuditID() string { return newID(auditPrefix) }

// NewToken returns a one-time slo_ bearer token and its stored hash.
func NewToken() (plaintext, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("operator: cannot read random bytes: %v", err))
	}
	plaintext = tokenPrefix + encoding.EncodeToString(b)
	return plaintext, HashToken(plaintext)
}

// HashToken returns the lowercase SHA-256 digest stored for a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func tokenMatches(token, hash string) bool {
	got := HashToken(token)
	return subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1
}

// LooksLikeToken reports whether token has the named-operator prefix.
func LooksLikeToken(token string) bool { return strings.HasPrefix(token, tokenPrefix) }

type identityKey struct{}

// WithIdentity attaches an authenticated operator to a request context.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFromContext returns the authenticated operator, when present.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}
