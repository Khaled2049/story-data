-- +goose Up
CREATE INDEX competitions_admin_created_id_idx ON competitions (created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS competitions_admin_created_id_idx;
