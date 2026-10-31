-- +goose Up
-- An event's place among a chapter's planned events, across plot lines.
-- Meaningful only while chapter_id is set. No CHECK ties the two: the
-- chapter FK's ON DELETE SET NULL would leave a position behind and fail it.
ALTER TABLE plot_events ADD COLUMN chapter_position NUMERIC(20,10);
CREATE UNIQUE INDEX plot_events_chapter_position_idx
  ON plot_events (chapter_id, chapter_position)
  WHERE chapter_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS plot_events_chapter_position_idx;
ALTER TABLE plot_events DROP COLUMN IF EXISTS chapter_position;
