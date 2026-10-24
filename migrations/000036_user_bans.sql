-- +goose Up
-- Members an admin has banned from the platform. Every authenticated request
-- checks this table, so a ban takes effect before the member's ID token expires.
CREATE TABLE user_bans (
  user_id TEXT PRIMARY KEY,
  reason TEXT CHECK (reason IS NULL OR char_length(reason) <= 500),
  banned_by TEXT NOT NULL,
  banned_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS user_bans;
