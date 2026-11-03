-- +goose Up
-- Events no longer have prerequisites or a time constraint.
DROP TABLE IF EXISTS plot_event_dependencies;
ALTER TABLE plot_events DROP COLUMN IF EXISTS time_constraint;

-- +goose Down
ALTER TABLE plot_events ADD COLUMN time_constraint JSONB;
CREATE TABLE plot_event_dependencies (
  plot_event_id UUID NOT NULL REFERENCES plot_events(id) ON DELETE CASCADE,
  depends_on_event_id UUID NOT NULL REFERENCES plot_events(id) ON DELETE CASCADE,
  relationship_type TEXT NOT NULL DEFAULT 'requires' CHECK (relationship_type = 'requires'),
  description TEXT,
  out_of_order_ok BOOLEAN NOT NULL DEFAULT false,
  PRIMARY KEY (plot_event_id, depends_on_event_id),
  CHECK (plot_event_id <> depends_on_event_id)
);
CREATE INDEX plot_event_dependencies_target_idx ON plot_event_dependencies (depends_on_event_id);
