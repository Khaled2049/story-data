package store

import "context"

// writeAdminAudit takes an execer so a delete can record itself in its own
// transaction: the row and the deletion commit or roll back together.
func writeAdminAudit(ctx context.Context, q execer, actor, action, targetID string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := q.Exec(ctx, `INSERT INTO admin_audit (actor_id, action, target_id, detail) VALUES ($1,$2,$3,$4)`, actor, action, targetID, detail)
	return err
}
