package operator

import (
	"context"
	"testing"
)

func TestRoleHierarchy(t *testing.T) {
	t.Parallel()
	if !RoleAdmin.Allows(RoleViewer) || !RoleAdmin.Allows(RoleOperator) || !RoleOperator.Allows(RoleViewer) {
		t.Fatal("higher roles do not inherit lower-role permissions")
	}
	if RoleViewer.Allows(RoleOperator) || RoleOperator.Allows(RoleAdmin) || Role("owner").Valid() {
		t.Fatal("role hierarchy grants an invalid permission")
	}
}

func TestOperatorTokenIsOpaqueAndHashed(t *testing.T) {
	t.Parallel()
	token, hash := NewToken()
	if !LooksLikeToken(token) || token == hash || !tokenMatches(token, hash) {
		t.Fatal("generated token does not have the expected one-way representation")
	}
	if tokenMatches(token+"x", hash) {
		t.Fatal("a modified token matched")
	}
}

func TestIdentityContext(t *testing.T) {
	t.Parallel()
	want := Identity{ID: "opr_a", Name: "alice", Role: RoleAdmin}
	got, ok := IdentityFromContext(WithIdentity(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("IdentityFromContext = (%+v, %v)", got, ok)
	}
}
