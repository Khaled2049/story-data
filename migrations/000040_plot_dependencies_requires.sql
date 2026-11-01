-- +goose Up
-- One authoritative direction: an event "requires" its prerequisites, and the
-- reverse ("enables") is derived on read. Other types were never used.
UPDATE plot_event_dependencies SET relationship_type = 'requires' WHERE relationship_type <> 'requires';
ALTER TABLE plot_event_dependencies
  ADD CONSTRAINT plot_event_dependencies_requires CHECK (relationship_type = 'requires');
-- The writer accepted a prerequisite that comes later in the manuscript
-- (a flashback or a reveal), so it isn't flagged.
ALTER TABLE plot_event_dependencies ADD COLUMN out_of_order_ok BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX plot_event_dependencies_target_idx ON plot_event_dependencies (depends_on_event_id);

-- +goose Down
DROP INDEX IF EXISTS plot_event_dependencies_target_idx;
ALTER TABLE plot_event_dependencies DROP COLUMN IF EXISTS out_of_order_ok;
ALTER TABLE plot_event_dependencies DROP CONSTRAINT IF EXISTS plot_event_dependencies_requires;
