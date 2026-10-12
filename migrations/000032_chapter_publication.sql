-- +goose Up
-- A chapter is a private draft until its author releases it; NULL means draft.
-- Before this a published story served every chapter row as it was typed.
-- Chapters that already exist were readable under that rule, so they stay so.
ALTER TABLE chapters ADD COLUMN published_at TIMESTAMPTZ;
UPDATE chapters SET published_at = created_at;
CREATE INDEX chapters_story_published_idx ON chapters (story_id, position) WHERE published_at IS NOT NULL;

-- updated_at moves on every draft save, which is right for the owner's shelf
-- and wrong for discovery: private typing would keep a serial at the top with
-- nothing new to read. public_updated_at moves only on reader-visible changes.
ALTER TABLE stories ADD COLUMN public_updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
UPDATE stories SET public_updated_at = updated_at;

-- Discovery and the sitemap now order by public_updated_at. The index from
-- 000001 was already covered by stories_public_updated_id_idx, so it is not
-- rebuilt.
DROP INDEX IF EXISTS stories_public_updated_idx;
DROP INDEX IF EXISTS stories_public_updated_id_idx;
DROP INDEX IF EXISTS stories_public_category_updated_id_idx;
CREATE INDEX stories_public_updated_id_idx
  ON stories (public_updated_at DESC, id DESC)
  WHERE is_published;
CREATE INDEX stories_public_category_updated_id_idx
  ON stories (category, public_updated_at DESC, id DESC)
  WHERE is_published;

-- +goose Down
DROP INDEX IF EXISTS stories_public_category_updated_id_idx;
DROP INDEX IF EXISTS stories_public_updated_id_idx;
CREATE INDEX stories_public_updated_idx ON stories (updated_at DESC) WHERE is_published;
CREATE INDEX stories_public_updated_id_idx
  ON stories (updated_at DESC, id DESC)
  WHERE is_published;
CREATE INDEX stories_public_category_updated_id_idx
  ON stories (category, updated_at DESC, id DESC)
  WHERE is_published;
ALTER TABLE stories DROP COLUMN IF EXISTS public_updated_at;
DROP INDEX IF EXISTS chapters_story_published_idx;
ALTER TABLE chapters DROP COLUMN IF EXISTS published_at;
