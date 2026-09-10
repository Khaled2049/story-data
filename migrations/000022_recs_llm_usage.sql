-- +goose Up
-- Durable per-user daily ceiling for the recommendation service's LLM paths.
--
-- recs calls Gemini directly, so creditProxy is not in that path: neither
-- PLATFORM_DAILY_REQUEST_LIMIT nor the per-user MAX_AI_USAGE applies to a HyDE
-- search or an explanation. Until this table existed the only guard was recs's
-- in-process token bucket, which is per-instance by construction — the ceiling
-- an operator configures is really `instances x limit`, and it resets whenever
-- Cloud Run recycles a container.
--
-- A row per (user, UTC day, kind) is the same shape as `indexing_usage`
-- (migration 000013), for the same reason: a counter that has to survive a
-- restart and be shared across instances belongs in the database.
--
-- The table is written by recs on the *write* pool. That is a deliberate
-- exception to "serving never touches the primary": the counter has to be
-- durable and consistent across instances to mean anything, and it costs at
-- most one small upsert per metered request, which the ceiling itself bounds.

CREATE TABLE recommendations.llm_usage (
  user_id    TEXT NOT NULL,
  -- UTC, assigned by the database so instances in different zones cannot
  -- disagree about when the budget resets.
  day        DATE NOT NULL,
  -- Which budget this row counts: 'search' (HyDE query expansion) or
  -- 'explain' (the explanation layer). Separate rows so a reader clicking
  -- "Why this story?" cannot consume their search budget.
  kind       TEXT NOT NULL,
  call_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day, kind)
);

-- Supports pruning old days. The primary key leads with user_id, so it cannot
-- serve a range scan over `day` on its own.
CREATE INDEX llm_usage_day_idx ON recommendations.llm_usage (day);

-- 000020's ALTER DEFAULT PRIVILEGES already covers tables created after it, but
-- only when `recs_service` existed at the time it ran — and goose records that
-- guarded migration as applied even where the role was absent. Restating the
-- grant here is idempotent and removes the ordering footgun.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'recs_service') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE
      ON TABLE recommendations.llm_usage TO recs_service;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS recommendations.llm_usage;
