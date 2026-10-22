-- +goose Up
-- Durable record of admins reading or deleting other users' content. Rows are
-- written by story-data only, and deletes write theirs in the same transaction.
CREATE TABLE admin_audit (
  id BIGSERIAL PRIMARY KEY,
  actor_id TEXT NOT NULL,
  action TEXT NOT NULL CHECK (action IN ('story.view', 'chapter.view', 'story.delete', 'competition.view', 'competition.delete')),
  target_id UUID NOT NULL,
  detail JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX admin_audit_created_idx ON admin_audit (created_at DESC);
CREATE INDEX admin_audit_actor_idx ON admin_audit (actor_id, created_at DESC);
CREATE INDEX admin_audit_target_idx ON admin_audit (target_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS admin_audit;
