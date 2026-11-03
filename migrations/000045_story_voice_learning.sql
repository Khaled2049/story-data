-- +goose Up
ALTER TABLE story_voice_profiles
  ADD COLUMN learn_from_writing BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN reference_ranges JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD CONSTRAINT story_voice_references_array CHECK (jsonb_typeof(reference_ranges) = 'array');

ALTER TABLE story_voice_profile_changes
  DROP CONSTRAINT story_voice_profile_changes_action_check,
  ADD CONSTRAINT story_voice_profile_changes_action_check CHECK (action IN ('save', 'agent', 'undo')),
  ADD COLUMN reason TEXT NOT NULL DEFAULT '',
  ADD COLUMN evidence JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE story_voice_agent_requests (
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  idempotency_key UUID NOT NULL,
  request_hash TEXT NOT NULL,
  change_id UUID NOT NULL REFERENCES story_voice_profile_changes(id) ON DELETE CASCADE,
  revision_after BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (story_id, idempotency_key)
);

-- +goose Down
DROP TABLE story_voice_agent_requests;
ALTER TABLE story_voice_profile_changes DROP COLUMN evidence, DROP COLUMN reason,
  DROP CONSTRAINT story_voice_profile_changes_action_check,
  ADD CONSTRAINT story_voice_profile_changes_action_check CHECK (action IN ('save', 'undo'));
ALTER TABLE story_voice_profiles DROP CONSTRAINT story_voice_references_array,
  DROP COLUMN reference_ranges, DROP COLUMN learn_from_writing;
