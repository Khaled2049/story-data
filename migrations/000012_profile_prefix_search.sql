-- +goose Up
-- Username prefix search runs as `username_lower LIKE 'term%'`. Under a
-- non-C collation the default btree index on username_lower cannot serve a
-- LIKE, so this pattern-ops index is what keeps the search off a sequential
-- scan as the profile table grows.
CREATE INDEX public_profiles_username_prefix_idx
  ON public_profiles (username_lower text_pattern_ops);

-- +goose Down
DROP INDEX IF EXISTS public_profiles_username_prefix_idx;
