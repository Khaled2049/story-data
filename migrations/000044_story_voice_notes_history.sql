-- +goose Up
ALTER TABLE story_voice_profiles
  ADD COLUMN voice_notes JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD CONSTRAINT story_voice_notes_array CHECK (jsonb_typeof(voice_notes) = 'array');

-- Preserve the free-text guidance saved by the first voice-profile slice.
UPDATE story_voice_profiles
SET voice_notes = jsonb_build_array(jsonb_build_object(
  'id', gen_random_uuid()::text,
  'text', notes,
  'origin', 'writer',
  'pinned', false
))
WHERE btrim(notes) <> '';

CREATE TABLE story_voice_profile_changes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  revision_before BIGINT NOT NULL CHECK (revision_before >= 0),
  revision_after BIGINT NOT NULL CHECK (revision_after > revision_before),
  action TEXT NOT NULL CHECK (action IN ('save', 'undo')),
  before_state JSONB NOT NULL,
  after_state JSONB NOT NULL,
  undo_of UUID REFERENCES story_voice_profile_changes(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX story_voice_changes_story_revision_idx
  ON story_voice_profile_changes (story_id, revision_after);

-- +goose Down
DROP TABLE IF EXISTS story_voice_profile_changes;
ALTER TABLE story_voice_profiles DROP CONSTRAINT IF EXISTS story_voice_notes_array;
ALTER TABLE story_voice_profiles DROP COLUMN IF EXISTS voice_notes;
