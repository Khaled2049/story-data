-- +goose Up
CREATE INDEX stories_admin_updated_id_idx ON stories (updated_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS stories_admin_updated_id_idx;
