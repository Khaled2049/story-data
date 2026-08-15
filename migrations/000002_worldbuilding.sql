-- +goose Up
ALTER TABLE plot_events ADD COLUMN chapter_number INTEGER CHECK (chapter_number >= 1);
CREATE INDEX characters_story_name_idx ON characters (story_id, name);
CREATE INDEX places_story_name_idx ON places (story_id, name);
CREATE INDEX plot_lines_story_created_idx ON plot_lines (story_id, created_at);
CREATE INDEX plot_events_line_position_idx ON plot_events (plot_line_id, position);

-- +goose Down
DROP INDEX IF EXISTS plot_events_line_position_idx;
DROP INDEX IF EXISTS plot_lines_story_created_idx;
DROP INDEX IF EXISTS places_story_name_idx;
DROP INDEX IF EXISTS characters_story_name_idx;
ALTER TABLE plot_events DROP COLUMN IF EXISTS chapter_number;
