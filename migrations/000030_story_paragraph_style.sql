-- +goose Up
-- How the author wants paragraphs set apart, for the editor and the public
-- reader alike: a gap between them, or no gap and an indented first line.
ALTER TABLE stories
  ADD COLUMN paragraph_style TEXT NOT NULL DEFAULT 'spaced'
  CONSTRAINT stories_paragraph_style_valid CHECK (paragraph_style IN ('spaced', 'indented'));

-- +goose Down
ALTER TABLE stories DROP COLUMN IF EXISTS paragraph_style;
