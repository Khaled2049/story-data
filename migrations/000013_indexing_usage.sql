-- +goose Up
CREATE TABLE indexing_usage (
  user_id TEXT NOT NULL,
  day DATE NOT NULL,
  pass_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day)
);

-- +goose Down
DROP TABLE IF EXISTS indexing_usage;
