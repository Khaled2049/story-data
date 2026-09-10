-- +goose Up
-- Platform-wide daily ceiling for the recommendation service's LLM paths.
--
-- `llm_usage` (000022) bounds one reader's day. It does not bound the platform's:
-- per-user budgets multiply by the user count, so the total is whatever signup
-- growth makes it. This is the counter with no user in the key, and it is the
-- direct analogue of creditProxy's PLATFORM_DAILY_REQUEST_LIMIT — which cannot
-- cover these routes, because recs calls Gemini without going through it.
--
-- It matters more here than the arithmetic suggests: the Gemini project is shared
-- with the story agent, so an unbounded recommendation spike does not merely cost
-- money, it degrades chapter generation.
--
-- Keyed by kind as well as day, so the two features fail independently — a flood
-- of searches must not take explanations down with it. The bound on total LLM
-- requests in a day is therefore the sum of the configured budgets, not either
-- one alone.
--
-- One row per kind per day, so every metered request contends on the same row.
-- That is a hot row by construction, and deliberately so: the contention is
-- bounded by the ceiling the row itself enforces.

CREATE TABLE recommendations.llm_platform_usage (
  -- UTC, assigned by the database, as in llm_usage.
  day        DATE NOT NULL,
  kind       TEXT NOT NULL,
  call_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, kind)
);

-- See 000022: restated rather than left to 000020's default privileges, which
-- only apply where `recs_service` existed at the time that migration ran.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'recs_service') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE
      ON TABLE recommendations.llm_platform_usage TO recs_service;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS recommendations.llm_platform_usage;
