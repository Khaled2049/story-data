-- +goose Up
-- Catalog ingest runs as recs_service but reads the published-story source
-- tables directly. Keep this grant read-only and limited to those four tables;
-- reader signals continue to cross the service boundary only through
-- recommendations.interactions.
--
-- As in 000020, local and test databases do not create this production role.
-- Production must create recs_service before applying this migration.

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'recs_service') THEN
    GRANT SELECT ON TABLE
      public.stories,
      public.story_tags,
      public.chapters,
      public.chapter_summaries
    TO recs_service;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'recs_service') THEN
    REVOKE SELECT ON TABLE
      public.stories,
      public.story_tags,
      public.chapters,
      public.chapter_summaries
    FROM recs_service;
  END IF;
END $$;
-- +goose StatementEnd
