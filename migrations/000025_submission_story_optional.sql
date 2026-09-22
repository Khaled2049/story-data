-- +goose Up
ALTER TABLE competition_submissions
  ALTER COLUMN story_id DROP NOT NULL;
ALTER TABLE competition_submissions
  DROP CONSTRAINT competition_submissions_story_id_fkey,
  ADD CONSTRAINT competition_submissions_story_id_fkey
    FOREIGN KEY (story_id) REFERENCES stories(id) ON DELETE SET NULL;

-- +goose Down
DELETE FROM competition_submissions WHERE story_id IS NULL;
ALTER TABLE competition_submissions
  DROP CONSTRAINT competition_submissions_story_id_fkey,
  ADD CONSTRAINT competition_submissions_story_id_fkey
    FOREIGN KEY (story_id) REFERENCES stories(id);
ALTER TABLE competition_submissions
  ALTER COLUMN story_id SET NOT NULL;
