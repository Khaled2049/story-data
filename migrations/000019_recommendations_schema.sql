-- +goose Up
-- The recommendation service's schema, moved here from its own database.
--
-- `taleTribe-recs` remains a separate service with its own connection; only the
-- schema moved. It lives here because `item_stats` and `interactions` are pure
-- derivations of `story_likes`, `story_ratings` and `reading_progress` — across
-- a database boundary that needs an export pipeline, and in one database it is
-- a query. story-data owns these migrations so that exactly one migration
-- runner ever touches this database.
--
-- The boundary that matters: recs *pulls* the catalog (published stories are
-- public data — ListPublicStories serves them to any caller) but never reads
-- reader signals itself. `reading_progress` is strictly private per-user data,
-- so the rows in `interactions` and `item_stats` are written by story-data and
-- recs only ever reads the `recommendations` schema. Grant its role USAGE on
-- this schema, plus USAGE on `public` for the `vector` type only — not SELECT
-- on any table in `public`.
--
-- Requires pgvector >= 0.8.0 for `hnsw.iterative_scan`, which is what keeps
-- recall usable when a metadata filter is applied alongside the KNN. CREATE
-- EXTENSION cannot express a minimum version; the recs `/health` endpoint
-- asserts it at runtime. `vector` and `pg_trgm` are already installed by
-- migrations 000003 and 000018 respectively, so they are not restated here.

CREATE SCHEMA recommendations;


-- ══════════════════════════════════════════════════════════════════════════
-- Catalog — one row per published story, embedded ONCE at ingestion.
-- Runtime embeds queries only; it never re-embeds catalog rows.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.items (
  id              BIGSERIAL PRIMARY KEY,
  -- A real foreign key, not the (source, source_id) pair this table carried
  -- when a CMU bootstrap corpus shared it. Unpublishing is handled by the
  -- ingest poll flipping is_eligible; deletion is handled here, so a deleted
  -- story cannot survive as a recommendable row.
  story_id        UUID NOT NULL UNIQUE REFERENCES stories(id) ON DELETE CASCADE,

  -- Normalized preprocessing schema. We never embed raw prose: it is full of
  -- incident, which swamps the premise/theme/tone signal the recommender
  -- actually ranks on. Derived by an LLM pass at ingest.
  title           TEXT NOT NULL,
  author          TEXT,
  genres          TEXT[] NOT NULL DEFAULT '{}',
  core_premise    TEXT,
  themes          TEXT[] NOT NULL DEFAULT '{}',
  tone            TEXT[] NOT NULL DEFAULT '{}',

  -- Filter metadata
  word_count      INTEGER,
  chapter_count   INTEGER,
  language        TEXT,
  target_audience TEXT,
  -- Backs the `published_after` filter. A story has no publication date of its
  -- own, so ingest fills this from the year the story row was created.
  published_year  INTEGER,

  -- Gates the partial HNSW index below. False for unpublished stories, and for
  -- rows whose normalization came back low-confidence — a thin description
  -- yields a confidently hallucinated premise, which is a poisoned vector that
  -- looks perfectly fine.
  is_eligible     BOOLEAN NOT NULL DEFAULT TRUE,
  confidence      REAL,

  -- Embedding provenance. embed_input is stored verbatim so a result can be
  -- explained and reproduced; its sha lets a re-run skip unchanged rows, which
  -- is what makes re-polling every published story cheap.
  embed_input     TEXT NOT NULL,
  embed_input_sha TEXT NOT NULL,
  embedding       vector(768),
  embed_model     TEXT,
  -- RETRIEVAL_DOCUMENT for every catalog row. Recorded because mixing task
  -- types across a corpus degrades recall silently.
  embed_task_type TEXT,
  embedded_at     TIMESTAMPTZ,

  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Partial HNSW index: ineligible rows never enter the graph at all, which
-- removes the highest-selectivity filter from the recall problem instead of
-- asking iterative_scan to solve it. Cheap here — the table is empty.
CREATE INDEX items_embedding_hnsw_idx ON recommendations.items
  USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 200)
  WHERE is_eligible;

CREATE INDEX items_genres_gin_idx ON recommendations.items USING gin (genres);
CREATE INDEX items_themes_gin_idx ON recommendations.items USING gin (themes);
-- Trigram, not prefix ops: readers misremember titles and skip subtitles, so
-- the "I liked these" path resolves what they typed by similarity.
CREATE INDEX items_title_trgm_idx ON recommendations.items USING gin (title gin_trgm_ops);
CREATE INDEX items_author_trgm_idx ON recommendations.items USING gin (author gin_trgm_ops);
CREATE INDEX items_word_count_idx ON recommendations.items (word_count) WHERE is_eligible;


-- ══════════════════════════════════════════════════════════════════════════
-- Scoring term 2 — popularity / engagement prior. Materialized on a schedule
-- because it is global (not per-request) and volume-damped arithmetic has no
-- business running inside the retrieval path.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.item_stats (
  item_id        BIGINT PRIMARY KEY REFERENCES recommendations.items(id) ON DELETE CASCADE,
  likes          INTEGER NOT NULL DEFAULT 0,
  ratings_count  INTEGER NOT NULL DEFAULT 0,
  avg_rating     REAL,
  completions    INTEGER NOT NULL DEFAULT 0,
  -- From stories.views, which counts one reader per day and needs no
  -- authentication. Carried for reference but weighted near-zero; see the
  -- popularity weights in `config` below.
  views          INTEGER NOT NULL DEFAULT 0,
  -- likes + ratings_count + completions. Drives the cold-start ramp α.
  n_interactions INTEGER NOT NULL DEFAULT 0,
  pop_score      REAL NOT NULL DEFAULT 0,
  refreshed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX item_stats_pop_idx ON recommendations.item_stats (pop_score DESC);


-- ══════════════════════════════════════════════════════════════════════════
-- Reader signals, derived by story-data from story_likes, story_ratings and
-- reading_progress. `completion` is never supplied by a source — it is derived
-- from progress against chapter position, so one definition is shared rather
-- than each caller inventing its own.
--
-- user_id is a Firebase uid, matching story_likes.user_id. No foreign key:
-- story-data has no users table, identity lives in Firebase Auth.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.interactions (
  user_id     TEXT NOT NULL,
  item_id     BIGINT NOT NULL REFERENCES recommendations.items(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('like', 'rating', 'progress', 'completion')),
  weight      REAL NOT NULL,           -- engagement strength used by the CF seed set
  value       REAL,                    -- rating 1..5, or scroll_percent 0..1
  occurred_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (user_id, item_id, kind)
);
CREATE INDEX interactions_item_idx ON recommendations.interactions (item_id);


-- ══════════════════════════════════════════════════════════════════════════
-- Scoring term 3 — item-item collaborative filtering.
--
-- STUB BY DESIGN. The table, the rebuild query and the scoring term are all
-- real, but `w_cf_ceiling` is 0 in `config` below, so CF contributes nothing
-- yet. The platform has no impression/click log — only binary likes, immutable
-- ratings, and reading_progress *current state* — so co-occurrence would be far
-- too sparse to beat the popularity prior. Keeping the shape means lighting it
-- up later is a config flip plus a backfill, not a rescoring rewrite.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.item_cooccurrence (
  item_a BIGINT NOT NULL REFERENCES recommendations.items(id) ON DELETE CASCADE,
  item_b BIGINT NOT NULL REFERENCES recommendations.items(id) ON DELETE CASCADE,
  cooc   INTEGER NOT NULL,  -- readers who engaged with both
  sim    REAL NOT NULL,     -- shrunk cosine: cooc / (sqrt(n_a * n_b) + lambda)
  PRIMARY KEY (item_a, item_b),
  -- One direction stored, both queried. Halves the table and makes the
  -- rebuild's symmetry a constraint rather than a convention.
  CHECK (item_a < item_b)
);
CREATE INDEX cooc_b_sim_idx ON recommendations.item_cooccurrence (item_b, sim DESC);


-- ══════════════════════════════════════════════════════════════════════════
-- Precomputed taste vector — the reason behavioral recs need no runtime
-- embedding call at all.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.user_taste (
  user_id             TEXT PRIMARY KEY,
  taste_embedding     vector(768) NOT NULL,
  -- Excluded from results (never recommend what they already read) and also
  -- the seed set S_u for the CF term.
  seed_item_ids       BIGINT[] NOT NULL,
  -- Items to actively suppress, e.g. anything rated <= 2.
  suppressed_item_ids BIGINT[] NOT NULL DEFAULT '{}',
  n_signals           INTEGER NOT NULL,
  computed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- ══════════════════════════════════════════════════════════════════════════
-- Explanation cache, keyed deterministically on
--   sha256(model | prompt_ver | story_id | embed_input_sha | query_fp)
-- so the same reader asking the same thing is free, and the key self-invalidates
-- when the item's normalized text or the prompt version changes.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.explanation_cache (
  cache_key   TEXT PRIMARY KEY,
  item_id     BIGINT NOT NULL REFERENCES recommendations.items(id) ON DELETE CASCADE,
  explanation TEXT NOT NULL,
  model       TEXT NOT NULL,
  prompt_ver  INTEGER NOT NULL,
  hit_count   INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- ══════════════════════════════════════════════════════════════════════════
-- Ingest bookkeeping — makes the LLM normalization pass resumable and skippable.
-- Keyed on the sha of the normalization input, so a crashed run re-reads the
-- cache and skips, and a prompt_ver bump forces regeneration.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.normalization_cache (
  summary_sha  TEXT PRIMARY KEY,
  core_premise TEXT,
  themes       TEXT[] NOT NULL DEFAULT '{}',
  tone         TEXT[] NOT NULL DEFAULT '{}',
  confidence   REAL,
  model        TEXT NOT NULL,
  prompt_ver   INTEGER NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- `cmu_backfill` is deliberately absent: the bootstrap corpus is gone and the
-- catalog is TaleTribe stories only.
CREATE TABLE recommendations.ingest_runs (
  id          BIGSERIAL PRIMARY KEY,
  kind        TEXT NOT NULL CHECK (kind IN ('platform_sync', 'stats_refresh', 'cooc_rebuild')),
  status      TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
  cursor      TEXT,   -- resume point
  counts      JSONB NOT NULL DEFAULT '{}'::jsonb,
  error       TEXT,
  started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ
);
CREATE INDEX ingest_runs_kind_started_idx ON recommendations.ingest_runs (kind, started_at DESC);


-- ══════════════════════════════════════════════════════════════════════════
-- Scoring knobs. In the database rather than env vars so ranking can be retuned
-- without a redeploy — and so a bad tune is one UPDATE away from being reverted.
-- ══════════════════════════════════════════════════════════════════════════
CREATE TABLE recommendations.config (
  key         TEXT PRIMARY KEY,
  value       REAL NOT NULL,
  description TEXT,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO recommendations.config (key, value, description) VALUES
  ('w_pop_ceiling',       0.25, 'Max popularity weight at full behavioral ramp'),
  ('w_cf_ceiling',        0.00, 'Max CF weight. 0 = stubbed; set 0.20 once an event log exists'),
  ('ramp_n_min',          5,    'Below this interaction count, scoring is 100% semantic'),
  ('ramp_n50',            20,   'Interaction count at which behavioral gets half its ceiling'),
  ('bayes_prior_c',       20,   'Bayesian rating prior weight; matches ramp_n50 by design'),
  ('cf_shrinkage_lambda', 10,   'Stops a single 1-of-1 co-occurrence from scoring 1.0'),
  ('pop_w_bayes',         0.60, 'Popularity sub-weight: volume-damped rating'),
  ('pop_w_engagement',    0.40, 'Popularity sub-weight: log-compressed likes + completions'),
  ('pop_completion_mult', 3.00, 'A completion is worth this many likes'),
  ('rrf_k',               60,   'Reciprocal Rank Fusion constant'),
  ('mmr_lambda',          0.70, 'MMR relevance/diversity tradeoff');

-- +goose Down
DROP SCHEMA IF EXISTS recommendations CASCADE;
