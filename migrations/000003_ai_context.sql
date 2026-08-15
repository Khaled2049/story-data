-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE chapter_summaries (
  chapter_id UUID PRIMARY KEY REFERENCES chapters(id) ON DELETE CASCADE,
  source_revision BIGINT NOT NULL CHECK (source_revision > 0),
  summary TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE story_vector_chunks (
  id UUID PRIMARY KEY,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  source_type TEXT NOT NULL CHECK (source_type IN ('chapter', 'character', 'place', 'plot_event')),
  source_id UUID NOT NULL,
  source_revision BIGINT NOT NULL CHECK (source_revision > 0),
  chunk_index INTEGER NOT NULL CHECK (chunk_index >= 0),
  text TEXT NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  embedding vector(768) NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source_type, source_id, source_revision, chunk_index)
);
CREATE INDEX story_vector_chunks_story_source_idx ON story_vector_chunks (story_id, source_type, source_id);
CREATE INDEX story_vector_chunks_embedding_hnsw_idx ON story_vector_chunks USING hnsw (embedding vector_cosine_ops);

ALTER TABLE indexing_outbox
  ADD COLUMN locked_at TIMESTAMPTZ,
  ADD COLUMN locked_by TEXT,
  ADD COLUMN lease_expires_at TIMESTAMPTZ;
CREATE INDEX indexing_outbox_lease_idx ON indexing_outbox (available_at, lease_expires_at)
  WHERE delivered_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS indexing_outbox_lease_idx;
ALTER TABLE indexing_outbox DROP COLUMN IF EXISTS lease_expires_at, DROP COLUMN IF EXISTS locked_by, DROP COLUMN IF EXISTS locked_at;
DROP INDEX IF EXISTS story_vector_chunks_embedding_hnsw_idx;
DROP INDEX IF EXISTS story_vector_chunks_story_source_idx;
DROP TABLE IF EXISTS story_vector_chunks;
DROP TABLE IF EXISTS chapter_summaries;
