-- +goose Up
ALTER TABLE public_profiles
  ADD COLUMN first_name TEXT NOT NULL DEFAULT '' CHECK (char_length(first_name) <= 50),
  ADD COLUMN last_name TEXT NOT NULL DEFAULT '' CHECK (char_length(last_name) <= 50),
  ADD COLUMN writing_interests TEXT NOT NULL DEFAULT '' CHECK (char_length(writing_interests) <= 200);

-- +goose Down
ALTER TABLE public_profiles
  DROP COLUMN first_name,
  DROP COLUMN last_name,
  DROP COLUMN writing_interests;
