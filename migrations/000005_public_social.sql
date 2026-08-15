-- +goose Up
CREATE INDEX chapter_comments_chapter_created_idx
  ON chapter_comments (chapter_id, created_at, id);

-- +goose Down
DROP INDEX IF EXISTS chapter_comments_chapter_created_idx;
