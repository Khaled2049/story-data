-- +goose Up
-- Least-privilege roles for every direct database user except the migrator.
--
-- Three NOLOGIN groups hold the grants. Logins with passwords are made by
-- terraform/database.tf, which runs after this migration and adds each login
-- to its group, so no credential lives in this repository:
--
--   story_data_app     -> login story_data_api: the API and sync-recs. Row
--                         reads and writes, no DDL, no TRUNCATE.
--   story_data_agents  -> login agents_service: taleTribe-agents' assistant
--                         context, search_story, voice learning and the
--                         indexing worker. Exactly the tables it queries.
--   story_data_recs    -> login recs_app: taleTribe-recs. Its own schema plus
--                         the published-catalog read.
--
-- Create those logins with SQL, never the Neon console: the console adds every
-- role it makes to neon_superuser, whose pg_read_all_data/pg_write_all_data
-- override everything granted here.
--
-- Roles are shared by every database on the server, so creation tolerates a
-- concurrent migration (parallel test databases) having made them first.

-- +goose StatementBegin
DO $$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['story_data_app', 'story_data_agents', 'story_data_recs'] LOOP
    BEGIN
      EXECUTE format('CREATE ROLE %I NOLOGIN', r);
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END LOOP;
END $$;
-- +goose StatementEnd

-- story_data_app: every row in both schemas, now and in later migrations.
GRANT USAGE ON SCHEMA public, recommendations TO story_data_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public, recommendations TO story_data_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public, recommendations TO story_data_app;
-- The API reads the migration version at startup; only the migrator writes it.
REVOKE INSERT, UPDATE, DELETE ON goose_db_version FROM story_data_app;
-- Applies to objects later created by the role running migrations.
ALTER DEFAULT PRIVILEGES IN SCHEMA public, recommendations
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO story_data_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public, recommendations
  GRANT USAGE, SELECT ON SEQUENCES TO story_data_app;

-- story_data_agents: named tables only, so a new table is invisible to agents
-- until a migration grants it. No profiles, reading history, messages or bans.
GRANT USAGE ON SCHEMA public TO story_data_agents;
GRANT SELECT ON stories, chapters, characters, places, plot_lines, plot_events,
  chapter_summaries TO story_data_agents;
GRANT SELECT, INSERT, UPDATE, DELETE ON story_vector_chunks TO story_data_agents;
GRANT SELECT, UPDATE ON indexing_outbox TO story_data_agents;
GRANT SELECT, INSERT, UPDATE ON indexing_usage, story_voice_learning_state TO story_data_agents;

-- story_data_recs: what 000020 meant recs_service to have, plus the catalog
-- read. The chapters grant is table-wide; recs' published_at filter is what
-- keeps drafts out (see taleTribe-recs ingest/platform.py).
GRANT USAGE ON SCHEMA public, recommendations TO story_data_recs;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA recommendations TO story_data_recs;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA recommendations TO story_data_recs;
ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO story_data_recs;
ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
  GRANT USAGE, SELECT ON SEQUENCES TO story_data_recs;
GRANT SELECT ON stories, story_tags, chapters, chapter_summaries TO story_data_recs;

-- Join each login to its group if it exists yet. Terraform also grants the
-- membership, which covers a login created after this migration ran.
-- +goose StatementBegin
DO $$
DECLARE pair text[];
BEGIN
  FOREACH pair SLICE 1 IN ARRAY ARRAY[
    ['story_data_api', 'story_data_app'],
    ['agents_service', 'story_data_agents'],
    ['recs_app', 'story_data_recs']
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = pair[1]) THEN
      EXECUTE format('GRANT %I TO %I', pair[2], pair[1]);
    END IF;
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Revokes this database's grants. The roles themselves are server-wide and may
-- hold grants in other databases, so they are left for an operator to drop.
ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
  REVOKE USAGE, SELECT ON SEQUENCES FROM story_data_recs;
ALTER DEFAULT PRIVILEGES IN SCHEMA recommendations
  REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM story_data_recs;
ALTER DEFAULT PRIVILEGES IN SCHEMA public, recommendations
  REVOKE USAGE, SELECT ON SEQUENCES FROM story_data_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public, recommendations
  REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM story_data_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public, recommendations FROM story_data_app, story_data_agents, story_data_recs;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public, recommendations FROM story_data_app, story_data_agents, story_data_recs;
REVOKE USAGE ON SCHEMA public, recommendations FROM story_data_app, story_data_agents, story_data_recs;
