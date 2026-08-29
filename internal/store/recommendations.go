package store

import "context"

// Reader signals, derived into the recommendations schema.
//
// This file is the only thing that reads product tables on behalf of the
// recommendation service, and that is the point. `reading_progress` is strictly
// private per-user data; if taleTribe-recs held a connection that could SELECT
// it, sharing a database would have quietly created a second, unaudited path to
// every reader's history. So story-data derives the rows and recs reads only
// `recommendations.*` — enforced by the role grant in migration 000020, not by
// convention.
//
// Everything downstream of `recommendations.interactions` stays in recs:
// `item_stats` aggregation, `pop_score`, `user_taste` and the co-occurrence
// rebuild all read that table and never leave the schema.

// completionScrollThreshold mirrors COMPLETION_SCROLL_THRESHOLD in
// taleTribe-recs (recommendation_engine/sync/interactions.py). A reader on the
// last chapter and near the bottom of it has finished the book; both halves are
// needed, since chapter index alone counts someone who opened the final chapter
// and stopped. TestRecommendationSignalWeights pins this and the weights below
// to those Python values — change one and the test tells you about the other.
const completionScrollThreshold = 0.9

// syntheticUserPrefix marks generated readers that taleTribe-recs writes
// directly for testing scoring before real traffic exists. The sync must not
// delete them and must not derive over them, so they are excluded from both
// halves. Firebase uids are 28-character alphanumerics and cannot collide.
const syntheticUserPrefix = `synth\_%`

// RecommendationSyncStats reports what one sync wrote.
type RecommendationSyncStats struct {
	Interactions int64 `json:"interactions"`
	Views        int64 `json:"views"`
}

// Derived in full each run rather than incrementally, because these are current
// *state* rather than an event log: un-liking a story deletes the row, and an
// incremental pass has nothing to observe. At the volumes involved that is one
// scan of three small tables, and it cannot drift.
const syncInteractionsSQL = `
INSERT INTO recommendations.interactions (user_id, item_id, kind, weight, value, occurred_at)
WITH chapter_rank AS (
  SELECT c.id AS chapter_id,
         row_number() OVER (PARTITION BY c.story_id ORDER BY c.position) AS idx,
         count(*)     OVER (PARTITION BY c.story_id)                     AS total
    FROM chapters c
), signals AS (
      SELECT sl.user_id, i.id AS item_id, 'like'::text AS kind,
             NULL::double precision AS value, sl.created_at AS occurred_at
        FROM story_likes sl
        JOIN recommendations.items i ON i.story_id = sl.story_id
  UNION ALL
      SELECT sr.user_id, i.id, 'rating', sr.rating::double precision, sr.created_at
        FROM story_ratings sr
        JOIN recommendations.items i ON i.story_id = sr.story_id
  UNION ALL
      SELECT rp.user_id, i.id, 'progress', rp.scroll_percent, rp.last_read_at
        FROM reading_progress rp
        JOIN recommendations.items i ON i.story_id = rp.story_id
  UNION ALL
      -- Completion is derived here and nowhere else, so every consumer shares
      -- one definition instead of each inventing its own.
      SELECT rp.user_id, i.id, 'completion', rp.scroll_percent, rp.last_read_at
        FROM reading_progress rp
        JOIN recommendations.items i  ON i.story_id = rp.story_id
        JOIN chapter_rank cr ON cr.chapter_id = rp.chapter_id
       WHERE cr.idx = cr.total AND rp.scroll_percent >= $1
)
SELECT user_id, item_id, kind,
       (CASE kind
          WHEN 'like'       THEN 1.0
          WHEN 'completion' THEN 0.8
          WHEN 'rating'     THEN CASE WHEN value >= 4 THEN 1.0
                                      WHEN value >= 3 THEN 0.4
                                      ELSE 0.0 END
          WHEN 'progress'   THEN 0.4 * least(1.0, greatest(0.0, coalesce(value, 0)))
          ELSE 0.0
        END)::real,
       value::real,
       occurred_at
  FROM signals
 WHERE user_id NOT LIKE $2
ON CONFLICT (user_id, item_id, kind) DO UPDATE SET
    weight      = EXCLUDED.weight,
    value       = EXCLUDED.value,
    occurred_at = EXCLUDED.occurred_at
`

// views is the one item_stats column recs cannot derive from interactions: it
// is an anonymous global counter on the story, not a per-reader signal. The
// upsert touches only that column, so a concurrent refresh in recs — which
// writes every other column and never this one — cannot be clobbered by it.
const syncViewsSQL = `
INSERT INTO recommendations.item_stats (item_id, views)
SELECT i.id, LEAST(s.views, 2147483647)
  FROM recommendations.items i
  JOIN stories s ON s.id = i.story_id
ON CONFLICT (item_id) DO UPDATE SET views = EXCLUDED.views
`

// SyncRecommendationSignals rebuilds the derived reader signals the
// recommendation service scores on.
//
// One transaction: a reader mid-sync must never see a catalog with likes
// deleted and not yet reinserted.
func (s *Store) SyncRecommendationSignals(ctx context.Context) (RecommendationSyncStats, error) {
	var stats RecommendationSyncStats
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback(ctx)

	// Scoped to non-synthetic readers so a generated cohort survives a sync.
	if _, err = tx.Exec(ctx,
		`DELETE FROM recommendations.interactions WHERE user_id NOT LIKE $1`,
		syntheticUserPrefix); err != nil {
		return stats, err
	}
	tag, err := tx.Exec(ctx, syncInteractionsSQL, completionScrollThreshold, syntheticUserPrefix)
	if err != nil {
		return stats, err
	}
	stats.Interactions = tag.RowsAffected()

	tag, err = tx.Exec(ctx, syncViewsSQL)
	if err != nil {
		return stats, err
	}
	stats.Views = tag.RowsAffected()

	return stats, tx.Commit(ctx)
}
