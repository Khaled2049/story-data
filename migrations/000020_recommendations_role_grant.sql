-- +goose Up
-- Confine taleTribe-recs to its own schema.
--
-- recs shares this database but must never read product tables: `reading_progress`
-- is strictly private per-user data, and `internal/store/recommendations.go`
-- derives the rows it is allowed to see into `recommendations.interactions`.
-- A grant is what makes that a boundary rather than an intention — without it,
-- co-locating the schemas silently hands recs a second, unaudited path to every
-- reader's history.
--
-- USAGE on `public` is required and is not a leak: it lets the role resolve the
-- `vector` type and the `hnsw`/`gin` operator classes, which the extensions
-- install there. It grants no access to any table, and no table grant is issued.
--
-- Guarded on the role existing so this is a no-op in local development and in
-- tests, where one superuser owns everything. Create the role per environment
-- (Terraform, phase 7) and this migration — or a re-run of it — does the rest.

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'recs_service') THEN
    GRANT USAGE ON SCHEMA public TO recs_service;
    GRANT USAGE ON SCHEMA recommendations TO recs_service;
    GRANT SELECT, INSERT, UPDATE, DELETE
      ON ALL TABLES IN SCHEMA recommendations TO recs_service;
    GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA recommendations TO recs_service;
    -- Tables added by a later migration are covered too; without this a new
    -- table would be invisible to recs until someone remembered to grant it.
    ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
      GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO recs_service;
    ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
      GRANT USAGE, SELECT ON SEQUENCES TO recs_service;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'recs_service') THEN
    ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
      REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM recs_service;
    ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
      REVOKE USAGE, SELECT ON SEQUENCES FROM recs_service;
    REVOKE ALL ON ALL SEQUENCES IN SCHEMA recommendations FROM recs_service;
    REVOKE ALL ON ALL TABLES IN SCHEMA recommendations FROM recs_service;
    REVOKE USAGE ON SCHEMA recommendations FROM recs_service;
    REVOKE USAGE ON SCHEMA public FROM recs_service;
  END IF;
END $$;
-- +goose StatementEnd
