-- +goose Up
-- Events link to a chapter by its id (chapter_id, from 000001) instead of a
-- typed number, so reordering or renaming chapters keeps the plan attached.
ALTER TABLE plot_events DROP COLUMN chapter_number;
ALTER TABLE plot_events ADD COLUMN status TEXT NOT NULL DEFAULT 'planned'
  CHECK (status IN ('planned', 'in_progress', 'written'));
CREATE INDEX plot_events_chapter_idx ON plot_events (chapter_id) WHERE chapter_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS plot_events_chapter_idx;
ALTER TABLE plot_events DROP COLUMN IF EXISTS status;
ALTER TABLE plot_events ADD COLUMN chapter_number INTEGER CHECK (chapter_number >= 1);
