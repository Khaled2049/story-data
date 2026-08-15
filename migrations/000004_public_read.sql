-- +goose Up
-- Cursor pagination uses the timestamp plus UUID tie-breaker. The partial
-- index keeps discovery queries scoped to published stories.
CREATE INDEX stories_public_category_updated_id_idx
  ON stories (category, updated_at DESC, id DESC)
  WHERE is_published;

CREATE INDEX stories_public_updated_id_idx
  ON stories (updated_at DESC, id DESC)
  WHERE is_published;

-- +goose Down
DROP INDEX IF EXISTS stories_public_updated_id_idx;
DROP INDEX IF EXISTS stories_public_category_updated_id_idx;
