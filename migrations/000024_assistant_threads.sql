-- +goose Up
-- Durable, story-scoped assistant conversations. The rich message payload is
-- kept as JSONB because assistant message parts are a versioned protocol: text,
-- tool calls, sources, and approval state must survive a reload without being
-- flattened into prose.
CREATE TABLE assistant_threads (
  id UUID PRIMARY KEY,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  title TEXT NOT NULL DEFAULT 'New conversation'
    CHECK (char_length(title) BETWEEN 1 AND 200),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  next_message_sequence BIGINT NOT NULL DEFAULT 1
    CHECK (next_message_sequence > 0),
  archived_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX assistant_threads_story_updated_idx
  ON assistant_threads (story_id, updated_at DESC, id DESC);

CREATE TABLE assistant_messages (
  id UUID PRIMARY KEY,
  thread_id UUID NOT NULL REFERENCES assistant_threads(id) ON DELETE CASCADE,
  sequence BIGINT NOT NULL CHECK (sequence > 0),
  role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
  parts JSONB NOT NULL CHECK (jsonb_typeof(parts) = 'array'),
  status TEXT NOT NULL DEFAULT 'complete'
    CHECK (status IN ('incomplete', 'complete', 'requires_action', 'failed', 'cancelled')),
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(metadata) = 'object'),
  idempotency_key TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 200),
  idempotency_fingerprint TEXT NOT NULL CHECK (char_length(idempotency_fingerprint) = 64),
  run_id TEXT CHECK (run_id IS NULL OR char_length(run_id) BETWEEN 1 AND 200),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (thread_id, sequence),
  UNIQUE (thread_id, idempotency_key)
);
CREATE INDEX assistant_messages_thread_sequence_idx
  ON assistant_messages (thread_id, sequence);

-- +goose Down
DROP TABLE IF EXISTS assistant_messages;
DROP TABLE IF EXISTS assistant_threads;
