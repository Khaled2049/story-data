-- +goose Up
CREATE TABLE reading_progress (
  user_id TEXT NOT NULL,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  chapter_id UUID NOT NULL REFERENCES chapters(id) ON DELETE CASCADE,
  scroll_percent DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (scroll_percent BETWEEN 0 AND 1),
  last_read_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, story_id)
);
CREATE INDEX reading_progress_user_recent_idx ON reading_progress (user_id, last_read_at DESC, story_id DESC);

-- +goose Down
DROP TABLE IF EXISTS reading_progress;
