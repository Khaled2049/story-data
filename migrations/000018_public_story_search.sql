-- +goose Up
-- Discovery search matches a term anywhere in the title or author name, so the
-- prefix-ops trick used for usernames (000012) does not apply — only trigrams
-- can serve a leading-wildcard ILIKE. The index expression must match the
-- predicate in ListPublicStories exactly, hence the same concatenation.
--
-- Partial on is_published: the public grid never looks at drafts, and drafts
-- are the bulk of the table, so this keeps the index small.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX stories_public_search_idx
  ON stories USING gin ((title || ' ' || author_name) gin_trgm_ops)
  WHERE is_published;

-- +goose Down
DROP INDEX IF EXISTS stories_public_search_idx;
