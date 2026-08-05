package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/operator"
)

func runKeysOperator(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected one of create, list, set-role, rotate, revoke, audit", errUsage)
	}
	action := args[0]
	fs := newFlagSet("keys operator "+action, stderr)
	storePath := storeFlag(fs)
	name := fs.String("name", "", "operator name or ID")
	role := fs.String("role", string(operator.RoleViewer), "viewer, operator, or admin")
	actorID := fs.String("actor", "", "filter audit records by operator ID")
	auditAction := fs.String("action", "", "filter audit records by exact action")
	since := fs.String("since", "", "filter audit records at or after RFC 3339 time")
	limit := fs.Int("limit", 100, "maximum audit records (1-1000)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: keys operator %s takes no positional arguments", errUsage, action)
	}

	ctx := context.Background()
	st, err := openStore(ctx, *storePath, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	switch action {
	case "create":
		if strings.TrimSpace(*name) == "" {
			return fmt.Errorf("%w: --name is required", errUsage)
		}
		r := operator.Role(*role)
		if !r.Valid() {
			return fmt.Errorf("%w: role %q is not viewer, operator, or admin", errUsage, *role)
		}
		token, hash := operator.NewToken()
		now := time.Now().UTC()
		op := operator.Operator{
			ID: operator.NewOperatorID(), Name: strings.TrimSpace(*name), TokenHash: hash,
			Role: r, CreatedAt: now, UpdatedAt: now,
		}
		if err := st.CreateOperator(ctx, op); err != nil {
			return err
		}
		if err := auditCLI(ctx, st, "create operator", op.ID); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Created operator %s (%s), role %s\n\n  %s\n\n", op.Name, op.ID, op.Role, token)
		fmt.Fprintln(stdout, "This token is shown once and is not recoverable. Store it now.")
		return nil

	case "list":
		ops, err := st.ListOperators(ctx)
		if err != nil {
			return err
		}
		if len(ops) == 0 {
			fmt.Fprintln(stdout, "No named operators. Create the first admin with:")
			fmt.Fprintln(stdout, "  spendlease keys operator create --name <name> --role admin")
			return nil
		}
		fmt.Fprintf(stdout, "%-32s %-24s %-10s %s\n", "ID", "NAME", "ROLE", "STATUS")
		for _, op := range ops {
			status := "active"
			if !op.Active() {
				status = "revoked"
			}
			fmt.Fprintf(stdout, "%-32s %-24s %-10s %s\n", op.ID, op.Name, op.Role, status)
		}
		return nil

	case "set-role", "rotate", "revoke":
		op, err := findOperator(ctx, st, strings.TrimSpace(*name))
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		switch action {
		case "set-role":
			r := operator.Role(*role)
			if !r.Valid() {
				return fmt.Errorf("%w: role %q is not viewer, operator, or admin", errUsage, *role)
			}
			if err := st.SetOperatorRole(ctx, op.ID, r, now); err != nil {
				return err
			}
			if err := auditCLI(ctx, st, "set operator role", op.ID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s now has the %s role.\n", op.Name, r)
			return nil
		case "rotate":
			token, hash := operator.NewToken()
			if err := st.RotateOperatorToken(ctx, op.ID, hash, now); err != nil {
				return err
			}
			if err := auditCLI(ctx, st, "rotate operator token", op.ID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Rotated the token for %s. The old token is invalid.\n\n  %s\n\n", op.Name, token)
			fmt.Fprintln(stdout, "This token is shown once and is not recoverable. Store it now.")
			return nil
		default:
			if err := st.DisableOperator(ctx, op.ID, now); err != nil {
				return err
			}
			if err := auditCLI(ctx, st, "revoke operator", op.ID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Revoked operator %s.\n", op.Name)
			return nil
		}

	case "audit":
		if *limit < 1 || *limit > 1000 {
			return fmt.Errorf("%w: --limit must be between 1 and 1000", errUsage)
		}
		filter := operator.AuditFilter{ActorID: strings.TrimSpace(*actorID), Action: strings.TrimSpace(*auditAction), Limit: *limit}
		if strings.TrimSpace(*since) != "" {
			filter.Since, err = time.Parse(time.RFC3339, strings.TrimSpace(*since))
			if err != nil {
				return fmt.Errorf("%w: --since must be an RFC 3339 timestamp", errUsage)
			}
		}
		records, err := st.ListOperatorAudit(ctx, filter)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%-30s %-12s %-10s %-6s %-40s %s\n", "TIME", "ACTOR", "PHASE", "HTTP", "ACTION", "REQUEST")
		for _, record := range records {
			status := "-"
			if record.StatusCode != 0 {
				status = strconv.Itoa(record.StatusCode)
			}
			fmt.Fprintf(stdout, "%-30s %-12s %-10s %-6s %-40s %s\n",
				record.CreatedAt.Format(time.RFC3339), record.ActorName, record.Phase, status, record.Action, record.RequestID)
		}
		return nil

	default:
		return fmt.Errorf("%w: unknown operator action %q", errUsage, action)
	}
}

type operatorFinder interface {
	ListOperators(context.Context) ([]operator.Operator, error)
}

func findOperator(ctx context.Context, st operatorFinder, nameOrID string) (operator.Operator, error) {
	if nameOrID == "" {
		return operator.Operator{}, fmt.Errorf("%w: --name is required", errUsage)
	}
	ops, err := st.ListOperators(ctx)
	if err != nil {
		return operator.Operator{}, err
	}
	for _, op := range ops {
		if op.ID == nameOrID || op.Name == nameOrID {
			return op, nil
		}
	}
	return operator.Operator{}, fmt.Errorf("no operator named %q", nameOrID)
}

func auditCLI(ctx context.Context, st interface {
	AppendOperatorAudit(context.Context, operator.AuditRecord) error
}, action, resource string) error {
	now := time.Now().UTC()
	return st.AppendOperatorAudit(ctx, operator.AuditRecord{
		ID: operator.NewAuditID(), RequestID: operator.NewAuditID(), ActorID: "local-cli", ActorName: "local-cli",
		Role: operator.RoleAdmin, Phase: "result", Action: action, Resource: resource, StatusCode: 200, CreatedAt: now,
	})
}
