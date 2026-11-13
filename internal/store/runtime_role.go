package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RowQuerier is satisfied by a pool, a connection or a transaction.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CheckRuntimeRole refuses a serving role that can change the schema or that
// inherits neon_superuser's read and write on every table. Production runs the
// API as a restricted login; a Neon-console role or the owner URL would pass
// every other check while silently undoing that.
func CheckRuntimeRole(ctx context.Context, q RowQuerier) error {
	var role string
	var super, createRole, bypassRLS, createInPublic, neonSuperuser bool
	err := q.QueryRow(ctx, `
		SELECT current_user, r.rolsuper, r.rolcreaterole, r.rolbypassrls,
		       has_schema_privilege(current_user, 'public', 'CREATE'),
		       COALESCE((SELECT pg_has_role(current_user, n.oid, 'MEMBER')
		                   FROM pg_roles n WHERE n.rolname = 'neon_superuser'), false)
		  FROM pg_roles r WHERE r.rolname = current_user`).
		Scan(&role, &super, &createRole, &bypassRLS, &createInPublic, &neonSuperuser)
	if err != nil {
		return fmt.Errorf("checking the database role: %w", err)
	}
	var problems []string
	for _, p := range []struct {
		bad  bool
		what string
	}{
		{super, "is a superuser"},
		{createRole, "can create roles"},
		{bypassRLS, "bypasses row-level security"},
		{createInPublic, "can create objects in schema public"},
		{neonSuperuser, "is a member of neon_superuser"},
	} {
		if p.bad {
			problems = append(problems, p.what)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("database role %s %s; serve as story_data_api (docs/database-roles.md)",
			role, strings.Join(problems, ", "))
	}
	return nil
}
