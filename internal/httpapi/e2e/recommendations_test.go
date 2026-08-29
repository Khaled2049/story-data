package e2e

// Derived reader signals for taleTribe-recs.
//
// story-data owns this derivation because `reading_progress` is private
// per-user data and recs must never read it directly — see
// `internal/store/recommendations.go`. These tests go through the store rather
// than HTTP because there is no endpoint: the sync is a scheduled job
// (`story-data sync-recs`), not a request.

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

// seedCatalog creates a published story with `chapters` chapters, plus the
// `recommendations.items` row that the derivation joins against. Returns the
// story id, the ordered chapter ids, and the catalog item id.
func seedCatalog(t *testing.T, title string, chapters int) (string, []string, int64) {
	t.Helper()
	ctx := context.Background()
	storyID := uuid.NewString()

	if _, err := testPool.Exec(ctx,
		`INSERT INTO stories (id, owner_id, title, description, author_name, category,
		 is_published, views) VALUES ($1,'owner',$2,'A description.','An Author','fantasy',true,7)`,
		storyID, title); err != nil {
		t.Fatalf("seed story: %v", err)
	}

	chapterIDs := make([]string, 0, chapters)
	for i := range chapters {
		id := uuid.NewString()
		if _, err := testPool.Exec(ctx,
			`INSERT INTO chapters (id, story_id, title, content, position, word_count)
			 VALUES ($1,$2,'Chapter','x',$3,100)`, id, storyID, i+1); err != nil {
			t.Fatalf("seed chapter: %v", err)
		}
		chapterIDs = append(chapterIDs, id)
	}

	var itemID int64
	if err := testPool.QueryRow(ctx,
		`INSERT INTO recommendations.items
		   (story_id, title, embed_input, embed_input_sha, embed_model, embed_task_type)
		 VALUES ($1,$2,'x',$3,'mock','RETRIEVAL_DOCUMENT') RETURNING id`,
		storyID, title, "sha-"+title).Scan(&itemID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return storyID, chapterIDs, itemID
}

// weight and value are `real` columns, so a value that is exact in Python
// arrives here as the nearest float32. Comparing to an epsilon keeps the tests
// about the formula rather than about IEEE 754.
func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-6 }

func syncSignals(t *testing.T) store.RecommendationSyncStats {
	t.Helper()
	stats, err := store.New(testPool).SyncRecommendationSignals(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return stats
}

// interactionRow reads back one derived signal.
func interactionRow(t *testing.T, user string, itemID int64, kind string) (float64, *float64, bool) {
	t.Helper()
	var weight float64
	var value *float64
	err := testPool.QueryRow(context.Background(),
		`SELECT weight, value FROM recommendations.interactions
		  WHERE user_id=$1 AND item_id=$2 AND kind=$3`, user, itemID, kind).Scan(&weight, &value)
	if err != nil {
		return 0, nil, false
	}
	return weight, value, true
}

func TestSyncDerivesLikesRatingsAndProgress(t *testing.T) {
	reset(t)
	ctx := context.Background()
	storyID, chapterIDs, itemID := seedCatalog(t, "The Salt Road", 3)

	if _, err := testPool.Exec(ctx,
		`INSERT INTO story_likes (story_id, user_id) VALUES ($1,'reader-a')`, storyID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO story_ratings (story_id, user_id, rating) VALUES ($1,'reader-a',5)`, storyID); err != nil {
		t.Fatal(err)
	}
	// Mid-book: chapter 2 of 3, so no completion.
	if _, err := testPool.Exec(ctx,
		`INSERT INTO reading_progress (user_id, story_id, chapter_id, scroll_percent)
		 VALUES ('reader-a',$1,$2,0.5)`, storyID, chapterIDs[1]); err != nil {
		t.Fatal(err)
	}

	syncSignals(t)

	if w, _, ok := interactionRow(t, "reader-a", itemID, "like"); !ok || !closeTo(w, 1.0) {
		t.Fatalf("like weight = %v (found=%v), want 1.0", w, ok)
	}
	if w, v, ok := interactionRow(t, "reader-a", itemID, "rating"); !ok || !closeTo(w, 1.0) || !closeTo(*v, 5) {
		t.Fatalf("rating = weight %v value %v (found=%v), want 1.0 / 5", w, v, ok)
	}
	if w, v, ok := interactionRow(t, "reader-a", itemID, "progress"); !ok || !closeTo(w, 0.2) || !closeTo(*v, 0.5) {
		t.Fatalf("progress = weight %v value %v (found=%v), want 0.2 / 0.5", w, *v, ok)
	}
	if _, _, ok := interactionRow(t, "reader-a", itemID, "completion"); ok {
		t.Fatal("mid-book progress must not derive a completion")
	}
}

func TestCompletionNeedsLastChapterAndDeepScroll(t *testing.T) {
	reset(t)
	ctx := context.Background()
	storyID, chapterIDs, itemID := seedCatalog(t, "Tide Logic", 3)

	// Both halves of the rule are load-bearing: chapter index alone counts a
	// reader who opened the final chapter and stopped.
	for _, tc := range []struct {
		user    string
		chapter string
		scroll  float64
		want    bool
	}{
		{"finished", chapterIDs[2], 0.95, true},
		{"opened-last-then-stopped", chapterIDs[2], 0.10, false},
		{"deep-in-the-middle", chapterIDs[1], 0.99, false},
		{"exactly-at-threshold", chapterIDs[2], 0.90, true},
	} {
		if _, err := testPool.Exec(ctx,
			`INSERT INTO reading_progress (user_id, story_id, chapter_id, scroll_percent)
			 VALUES ($1,$2,$3,$4)`, tc.user, storyID, tc.chapter, tc.scroll); err != nil {
			t.Fatal(err)
		}
	}

	syncSignals(t)

	for _, tc := range []struct {
		user string
		want bool
	}{
		{"finished", true},
		{"opened-last-then-stopped", false},
		{"deep-in-the-middle", false},
		{"exactly-at-threshold", true},
	} {
		w, _, ok := interactionRow(t, tc.user, itemID, "completion")
		if ok != tc.want {
			t.Fatalf("%s: completion present = %v, want %v", tc.user, ok, tc.want)
		}
		if ok && !closeTo(w, 0.8) {
			t.Fatalf("%s: completion weight = %v, want 0.8", tc.user, w)
		}
	}
}

// TestRecommendationSignalWeights pins the SQL weight expression to
// `seed_engagement_weight` in taleTribe-recs
// (recommendation_engine/scoring.py). The two are separate implementations of
// one formula — recs cannot compute it, because it cannot read the source
// tables — so drift is only caught here.
func TestRecommendationSignalWeights(t *testing.T) {
	reset(t)
	ctx := context.Background()
	storyID, _, itemID := seedCatalog(t, "Weights", 1)

	// A rating of 2 or below is a *negative* signal: weight 0, and recs puts the
	// item on the reader's suppression list rather than treating it as a taste
	// anchor.
	for _, tc := range []struct {
		user   string
		rating int
		want   float64
	}{
		{"rated-5", 5, 1.0},
		{"rated-4", 4, 1.0},
		{"rated-3", 3, 0.4},
		{"rated-2", 2, 0.0},
		{"rated-1", 1, 0.0},
	} {
		if _, err := testPool.Exec(ctx,
			`INSERT INTO story_ratings (story_id, user_id, rating) VALUES ($1,$2,$3)`,
			storyID, tc.user, tc.rating); err != nil {
			t.Fatal(err)
		}
	}
	syncSignals(t)
	for _, tc := range []struct {
		user string
		want float64
	}{
		{"rated-5", 1.0}, {"rated-4", 1.0}, {"rated-3", 0.4},
		{"rated-2", 0.0}, {"rated-1", 0.0},
	} {
		w, _, ok := interactionRow(t, tc.user, itemID, "rating")
		if !ok || !closeTo(w, tc.want) {
			t.Fatalf("%s: weight = %v (found=%v), want %v", tc.user, w, ok, tc.want)
		}
	}
}

func TestSyncIsIdempotentAndReflectsRemovals(t *testing.T) {
	reset(t)
	ctx := context.Background()
	storyID, _, itemID := seedCatalog(t, "Removals", 1)

	if _, err := testPool.Exec(ctx,
		`INSERT INTO story_likes (story_id, user_id) VALUES ($1,'reader-a'),($1,'reader-b')`,
		storyID); err != nil {
		t.Fatal(err)
	}

	first := syncSignals(t)
	second := syncSignals(t)
	if first.Interactions != second.Interactions {
		t.Fatalf("re-running changed the row count: %d then %d",
			first.Interactions, second.Interactions)
	}

	// Un-liking deletes the source row. These are current state, not an event
	// log, so the derived row has to disappear with it.
	if _, err := testPool.Exec(ctx,
		`DELETE FROM story_likes WHERE story_id=$1 AND user_id='reader-b'`, storyID); err != nil {
		t.Fatal(err)
	}
	syncSignals(t)

	if _, _, ok := interactionRow(t, "reader-b", itemID, "like"); ok {
		t.Fatal("an un-liked story must not keep its derived interaction")
	}
	if _, _, ok := interactionRow(t, "reader-a", itemID, "like"); !ok {
		t.Fatal("the surviving like was removed too")
	}
}

func TestSyncPreservesSyntheticReaders(t *testing.T) {
	reset(t)
	ctx := context.Background()
	_, _, itemID := seedCatalog(t, "Synthetic", 1)

	// taleTribe-recs writes these directly to exercise scoring before real
	// traffic exists. A sync that deleted them would empty the only dataset
	// there is.
	if _, err := testPool.Exec(ctx,
		`INSERT INTO recommendations.interactions
		   (user_id, item_id, kind, weight, value, occurred_at)
		 VALUES ('synth_0042',$1,'like',1.0,NULL,now())`, itemID); err != nil {
		t.Fatal(err)
	}

	syncSignals(t)

	if _, _, ok := interactionRow(t, "synth_0042", itemID, "like"); !ok {
		t.Fatal("sync deleted a synthetic reader's signals")
	}
}

func TestSyncCopiesStoryViews(t *testing.T) {
	reset(t)
	_, _, itemID := seedCatalog(t, "Views", 1)

	// views is the one item_stats column recs cannot derive from interactions:
	// it is an anonymous counter on the story, not a per-reader signal.
	syncSignals(t)

	var views int
	if err := testPool.QueryRow(context.Background(),
		`SELECT views FROM recommendations.item_stats WHERE item_id=$1`, itemID).Scan(&views); err != nil {
		t.Fatalf("item_stats row missing: %v", err)
	}
	if views != 7 {
		t.Fatalf("views = %d, want 7", views)
	}
}

func TestSyncIgnoresStoriesOutsideTheCatalog(t *testing.T) {
	reset(t)
	ctx := context.Background()

	// A story with no recommendations.items row is not in the catalog — an
	// unpublished draft, or one the ingest has not reached yet. Its signals must
	// not be derived, or the item_id foreign key would have nothing to point at.
	storyID := uuid.NewString()
	if _, err := testPool.Exec(ctx,
		`INSERT INTO stories (id, owner_id, title, author_name, is_published)
		 VALUES ($1,'owner','Uncatalogued','An Author',true)`, storyID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO story_likes (story_id, user_id) VALUES ($1,'reader-a')`, storyID); err != nil {
		t.Fatal(err)
	}

	stats := syncSignals(t)

	if stats.Interactions != 0 {
		t.Fatalf("derived %d interactions for an uncatalogued story, want 0", stats.Interactions)
	}
}
