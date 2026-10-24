-- +goose Up
-- Admins can settle a competition from the admin page; the audit row is
-- written in the settlement transaction, so the action must be allowed here.
ALTER TABLE admin_audit DROP CONSTRAINT admin_audit_action_check;
ALTER TABLE admin_audit ADD CONSTRAINT admin_audit_action_check CHECK (action IN (
  'story.view', 'chapter.view', 'story.delete',
  'competition.view', 'competition.delete', 'competition.settle'
));

-- +goose Down
DELETE FROM admin_audit WHERE action = 'competition.settle';
ALTER TABLE admin_audit DROP CONSTRAINT admin_audit_action_check;
ALTER TABLE admin_audit ADD CONSTRAINT admin_audit_action_check CHECK (action IN (
  'story.view', 'chapter.view', 'story.delete', 'competition.view', 'competition.delete'
));
