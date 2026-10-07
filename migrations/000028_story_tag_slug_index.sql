-- +goose Up
-- Tag pages filter the public listing by a tag's URL form. The expression must
-- match the predicate in ListPublicStories (and store.TagSlug) exactly, or the
-- planner falls back to scanning story_tags.
CREATE INDEX story_tags_slug_idx
  ON story_tags ((replace(lower(tag), ' ', '-')), story_id);

-- +goose Down
DROP INDEX IF EXISTS story_tags_slug_idx;
