-- +goose Up
-- Events no longer carry a tension level; nothing reads or writes it.
ALTER TABLE plot_events DROP COLUMN IF EXISTS tension_level;

-- +goose Down
ALTER TABLE plot_events ADD COLUMN tension_level SMALLINT NOT NULL DEFAULT 5 CHECK (tension_level BETWEEN 1 AND 10);
