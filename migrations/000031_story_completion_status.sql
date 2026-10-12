-- +goose Up
-- Whether a story is finished is a separate fact from whether it is published:
-- a serial is public long before its last chapter. The date and note are the
-- author's optional word to readers about what comes next.
ALTER TABLE stories
  ADD COLUMN completion_status TEXT NOT NULL DEFAULT 'ongoing'
    CONSTRAINT stories_completion_status_valid CHECK (completion_status IN ('ongoing', 'completed')),
  ADD COLUMN next_chapter_on DATE,
  ADD COLUMN reader_note TEXT NOT NULL DEFAULT ''
    CONSTRAINT stories_reader_note_length CHECK (char_length(reader_note) <= 280),
  ADD COLUMN reader_note_updated_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE stories
  DROP COLUMN IF EXISTS reader_note_updated_at,
  DROP COLUMN IF EXISTS reader_note,
  DROP COLUMN IF EXISTS next_chapter_on,
  DROP COLUMN IF EXISTS completion_status;
