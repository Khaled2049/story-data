-- +goose Up
CREATE TABLE story_voice_profiles (
  story_id UUID PRIMARY KEY REFERENCES stories(id) ON DELETE CASCADE,
  point_of_view TEXT NOT NULL DEFAULT 'unspecified',
  tense TEXT NOT NULL DEFAULT 'unspecified',
  notes TEXT NOT NULL DEFAULT '',
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (point_of_view IN ('unspecified', 'first', 'close_third', 'omniscient', 'varies')),
  CHECK (tense IN ('unspecified', 'past', 'present', 'varies'))
);

-- +goose Down
DROP TABLE IF EXISTS story_voice_profiles;
