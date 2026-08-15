-- +goose Up
ALTER TABLE public_profiles ADD COLUMN guestbook_policy TEXT NOT NULL DEFAULT 'everyone'
  CHECK (guestbook_policy IN ('everyone','followers','following','mutuals','nobody'));

CREATE TABLE user_follows (
  follower_id TEXT NOT NULL,
  followed_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (follower_id, followed_id),
  CHECK (follower_id <> followed_id)
);
CREATE INDEX user_follows_followed_idx ON user_follows (followed_id, follower_id);

CREATE TABLE guestbook_entries (
  id UUID PRIMARY KEY,
  owner_id TEXT NOT NULL,
  author_id TEXT NOT NULL,
  content TEXT NOT NULL CHECK (char_length(content) BETWEEN 1 AND 10000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX guestbook_entries_owner_created_idx ON guestbook_entries (owner_id, created_at DESC, id DESC);

CREATE TABLE guestbook_replies (
  id UUID PRIMARY KEY,
  entry_id UUID NOT NULL REFERENCES guestbook_entries(id) ON DELETE CASCADE,
  parent_id UUID REFERENCES guestbook_replies(id) ON DELETE CASCADE,
  author_id TEXT NOT NULL,
  content TEXT NOT NULL CHECK (char_length(content) BETWEEN 1 AND 10000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX guestbook_replies_entry_created_idx ON guestbook_replies (entry_id, created_at DESC, id DESC);

CREATE TABLE guestbook_entry_votes (
  entry_id UUID NOT NULL REFERENCES guestbook_entries(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  vote TEXT NOT NULL CHECK (vote IN ('up','down')),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (entry_id, user_id)
);
CREATE TABLE guestbook_reply_votes (
  reply_id UUID NOT NULL REFERENCES guestbook_replies(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  vote TEXT NOT NULL CHECK (vote IN ('up','down')),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (reply_id, user_id)
);
CREATE TABLE guestbook_daily_usage (
  user_id TEXT NOT NULL,
  day DATE NOT NULL,
  entry_count INTEGER NOT NULL DEFAULT 0,
  reply_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day)
);

-- +goose Down
DROP TABLE IF EXISTS guestbook_daily_usage;
DROP TABLE IF EXISTS guestbook_reply_votes;
DROP TABLE IF EXISTS guestbook_entry_votes;
DROP TABLE IF EXISTS guestbook_replies;
DROP TABLE IF EXISTS guestbook_entries;
DROP TABLE IF EXISTS user_follows;
ALTER TABLE public_profiles DROP COLUMN IF EXISTS guestbook_policy;
