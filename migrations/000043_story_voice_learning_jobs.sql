-- +goose Up
CREATE TABLE story_voice_learning_jobs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  chapter_id UUID NOT NULL REFERENCES chapters(id) ON DELETE CASCADE,
  source_revision BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  available_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '2 minutes',
  lease_expires_at TIMESTAMPTZ,
  attempts INTEGER NOT NULL DEFAULT 0,
  processed_at TIMESTAMPTZ,
  UNIQUE (chapter_id, source_revision)
);
CREATE INDEX story_voice_learning_ready_idx ON story_voice_learning_jobs (available_at)
  WHERE processed_at IS NULL;
CREATE INDEX story_voice_learning_story_created_idx ON story_voice_learning_jobs (story_id,created_at DESC);
CREATE TABLE story_voice_learning_state (
  story_id UUID PRIMARY KEY REFERENCES stories(id) ON DELETE CASCADE,
  last_analyzed_at TIMESTAMPTZ NOT NULL DEFAULT '-infinity'::timestamptz,
  day DATE NOT NULL DEFAULT (now() AT TIME ZONE 'utc')::date,
  pass_count INTEGER NOT NULL DEFAULT 0
);

-- +goose StatementBegin
CREATE FUNCTION enqueue_story_voice_learning() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF char_length(NEW.content) >= 300 AND
     (TG_OP = 'INSERT' OR NEW.content IS DISTINCT FROM OLD.content) THEN
    INSERT INTO story_voice_learning_jobs (story_id,chapter_id,source_revision)
    VALUES (NEW.story_id,NEW.id,NEW.revision) ON CONFLICT DO NOTHING;
    DELETE FROM story_voice_learning_jobs WHERE story_id=NEW.story_id AND id IN (
      SELECT id FROM story_voice_learning_jobs WHERE story_id=NEW.story_id
      ORDER BY created_at DESC,id DESC OFFSET 50);
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER chapters_voice_learning AFTER INSERT OR UPDATE OF content ON chapters
  FOR EACH ROW EXECUTE FUNCTION enqueue_story_voice_learning();

-- +goose Down
DROP TRIGGER chapters_voice_learning ON chapters;
DROP FUNCTION enqueue_story_voice_learning();
DROP TABLE story_voice_learning_state;
DROP TABLE story_voice_learning_jobs;
