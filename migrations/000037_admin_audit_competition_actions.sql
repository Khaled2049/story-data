-- +goose Up
-- Admins can settle and cancel competitions from the admin page; each writes
-- its audit row in the same transaction as the money moves, so the actions
-- must be allowed here.
ALTER TABLE admin_audit DROP CONSTRAINT admin_audit_action_check;
ALTER TABLE admin_audit ADD CONSTRAINT admin_audit_action_check CHECK (action IN (
  'story.view', 'chapter.view', 'story.delete',
  'competition.view', 'competition.delete', 'competition.settle', 'competition.cancel'
));

-- +goose Down
DELETE FROM admin_audit WHERE action IN ('competition.settle', 'competition.cancel');
ALTER TABLE admin_audit DROP CONSTRAINT admin_audit_action_check;
ALTER TABLE admin_audit ADD CONSTRAINT admin_audit_action_check CHECK (action IN (
  'story.view', 'chapter.view', 'story.delete', 'competition.view', 'competition.delete'
));
