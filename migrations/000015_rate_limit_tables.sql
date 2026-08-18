-- +goose Up
-- Durable per-user daily budgets for the write paths that had none. The
-- in-process rate limiter bounds bursts per instance; these bound a user
-- platform-wide and survive a restart, which is what a quota has to do.
CREATE TABLE user_daily_usage (
  user_id TEXT NOT NULL,
  action TEXT NOT NULL,
  day DATE NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, action, day)
);

-- One view per reader per story per day. views used to be an unauthenticated
-- UPDATE with no dedup, so the platform's discovery signal could be set to any
-- number with a shell loop. viewer_key is a uid for a signed-in reader and a
-- salted hash of the client address otherwise — never a raw IP.
CREATE TABLE public_story_view_hits (
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  viewer_key TEXT NOT NULL,
  day DATE NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (story_id, viewer_key, day)
);

-- +goose Down
DROP TABLE IF EXISTS public_story_view_hits;
DROP TABLE IF EXISTS user_daily_usage;
