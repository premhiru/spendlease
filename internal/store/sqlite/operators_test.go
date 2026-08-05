package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/premhiru/spendlease/internal/operator"
	"github.com/premhiru/spendlease/internal/store"
)

func TestOperatorLifecycleAndFinalAdminInvariant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()
	newOperator := func(name string, role operator.Role) (operator.Operator, string) {
		token, hash := operator.NewToken()
		return operator.Operator{
			ID: operator.NewOperatorID(), Name: name, TokenHash: hash, Role: role,
			CreatedAt: now, UpdatedAt: now,
		}, token
	}

	alice, aliceToken := newOperator("alice", operator.RoleAdmin)
	if err := st.CreateOperator(ctx, alice); err != nil {
		t.Fatalf("CreateOperator(alice): %v", err)
	}
	if got, err := st.GetOperatorByTokenHash(ctx, operator.HashToken(aliceToken)); err != nil || got.ID != alice.ID {
		t.Fatalf("GetOperatorByTokenHash = (%+v, %v)", got, err)
	}
	if err := st.DisableOperator(ctx, alice.ID, now.Add(time.Minute)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("disabling final admin error = %v, want ErrConflict", err)
	}

	bob, _ := newOperator("bob", operator.RoleAdmin)
	if err := st.CreateOperator(ctx, bob); err != nil {
		t.Fatalf("CreateOperator(bob): %v", err)
	}
	if err := st.SetOperatorRole(ctx, alice.ID, operator.RoleViewer, now.Add(time.Minute)); err != nil {
		t.Fatalf("SetOperatorRole: %v", err)
	}
	newToken, newHash := operator.NewToken()
	if err := st.RotateOperatorToken(ctx, alice.ID, newHash, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("RotateOperatorToken: %v", err)
	}
	if _, err := st.GetOperatorByTokenHash(ctx, operator.HashToken(aliceToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old token lookup = %v, want ErrNotFound", err)
	}
	if got, err := st.GetOperatorByTokenHash(ctx, operator.HashToken(newToken)); err != nil || got.Role != operator.RoleViewer {
		t.Fatalf("new token lookup = (%+v, %v)", got, err)
	}
	if err := st.DisableOperator(ctx, alice.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("DisableOperator: %v", err)
	}
	got, _ := st.GetOperatorByTokenHash(ctx, newHash)
	if got.Active() {
		t.Fatal("disabled operator still reports active")
	}
}

func TestOperatorAuditIsAppendOnlyAndFilterable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()
	for i, actor := range []string{"opr_alice", "opr_bob"} {
		if err := st.AppendOperatorAudit(ctx, operator.AuditRecord{
			ID: operator.NewAuditID(), RequestID: "request", ActorID: actor, ActorName: actor,
			Role: operator.RoleAdmin, Phase: "result", Action: "POST /api/v1/runs", Resource: "run",
			StatusCode: 200 + i, CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("AppendOperatorAudit: %v", err)
		}
	}
	records, err := st.ListOperatorAudit(ctx, operator.AuditFilter{ActorID: "opr_bob", Limit: 10})
	if err != nil || len(records) != 1 || records[0].StatusCode != 201 {
		t.Fatalf("filtered audit = (%+v, %v)", records, err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE operator_audit SET actor_name = 'mallory'`); err == nil {
		t.Fatal("database allowed an operator audit update")
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM operator_audit`); err == nil {
		t.Fatal("database allowed operator audit deletion")
	}
}
