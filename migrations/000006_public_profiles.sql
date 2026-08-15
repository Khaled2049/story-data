-- +goose Up
CREATE TABLE public_profiles (
  user_id TEXT PRIMARY KEY,
  username TEXT NOT NULL CHECK (char_length(username) BETWEEN 3 AND 20),
  username_lower TEXT NOT NULL UNIQUE CHECK (username_lower = lower(username)),
  photo_url TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '' CHECK (char_length(bio) <= 300),
  occupation TEXT NOT NULL DEFAULT '' CHECK (char_length(occupation) <= 50),
  location TEXT NOT NULL DEFAULT '' CHECK (char_length(location) <= 50),
  wallet_address TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (username ~ '^[A-Za-z0-9_]+$'),
  CHECK (wallet_address IS NULL OR wallet_address ~ '^0x[0-9a-f]{40}$')
);
CREATE INDEX public_profiles_created_idx ON public_profiles (created_at DESC, user_id DESC);

-- +goose Down
DROP TABLE IF EXISTS public_profiles;
