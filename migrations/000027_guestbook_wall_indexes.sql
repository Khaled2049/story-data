-- +goose Up
-- The combined wall reads each followed author's newest entries through this
-- index, one bounded probe per author, instead of scanning every entry.
CREATE INDEX guestbook_entries_author_created_idx ON guestbook_entries (author_id, created_at DESC, id DESC);

-- A paged thread walks from its top-level replies down to their descendants,
-- and the ON DELETE CASCADE on parent_id looks children up the same way.
CREATE INDEX guestbook_replies_parent_idx ON guestbook_replies (parent_id) WHERE parent_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS guestbook_replies_parent_idx;
DROP INDEX IF EXISTS guestbook_entries_author_created_idx;
