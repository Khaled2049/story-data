package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const (
	alice = "user-alice"
	bob   = "user-bob"
)

func newStory(t *testing.T, uid, title string) map[string]any {
	t.Helper()
	return call(t, "POST", "/v1/stories", uid, map[string]any{
		"title": title, "description": "d", "authorName": "a", "tags": []string{"x"},
	}).expect(http.StatusCreated).json()
}

func TestStoryCreateAndList(t *testing.T) {
	reset(t)

	story := newStory(t, alice, "The Glass Cartographer")
	if story["ownerId"] != alice {
		t.Errorf("ownerId = %v, want %v", story["ownerId"], alice)
	}
	if story["title"] != "The Glass Cartographer" {
		t.Errorf("title = %v", story["title"])
	}
	if story["revision"].(float64) != 1 {
		t.Errorf("revision = %v, want 1", story["revision"])
	}
	if story["published"] != false {
		t.Errorf("new story should be unpublished, got %v", story["published"])
	}

	// Creating a story opens it with an empty first chapter.
	chapters := get(t, fmt.Sprintf("/v1/stories/%s/chapters", story["id"]), alice).
		expect(http.StatusOK).list()
	if len(chapters) != 1 {
		t.Fatalf("expected 1 auto-created chapter, got %d", len(chapters))
	}

	mine := get(t, "/v1/stories", alice).expect(http.StatusOK).list()
	if len(mine) != 1 {
		t.Fatalf("alice should see 1 story, got %d", len(mine))
	}

	// Listing is owner-scoped.
	theirs := get(t, "/v1/stories", bob).expect(http.StatusOK).list()
	if len(theirs) != 0 {
		t.Errorf("bob should see no stories, got %d", len(theirs))
	}
}

func TestStoryRequiresAuthAndTitle(t *testing.T) {
	reset(t)

	get(t, "/v1/stories", "").expect(http.StatusUnauthorized)
	call(t, "POST", "/v1/stories", "", map[string]any{"title": "x"}).expect(http.StatusUnauthorized)
	call(t, "POST", "/v1/stories", alice, map[string]any{"title": "   "}).expect(http.StatusBadRequest)
}

func TestStoryUpdateRequiresMatchingRevision(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Original")
	id := story["id"].(string)
	rev := int64(story["revision"].(float64))

	body := map[string]any{"title": "Renamed", "description": "d", "authorName": "a", "tags": []string{"x"}}

	// Missing If-Match is refused with 428, not silently applied.
	call(t, "PATCH", "/v1/stories/"+id, alice, body).expect(http.StatusPreconditionRequired)
	// A non-numeric revision is a malformed precondition, not a conflict.
	call(t, "PATCH", "/v1/stories/"+id, alice, body,
		map[string]string{"If-Match": "not-a-number"}).expect(http.StatusPreconditionRequired)

	updated := call(t, "PATCH", "/v1/stories/"+id, alice, body, ifMatch(rev)).
		expect(http.StatusOK).json()
	if updated["title"] != "Renamed" {
		t.Errorf("title = %v", updated["title"])
	}
	if int64(updated["revision"].(float64)) <= rev {
		t.Errorf("revision should advance past %d, got %v", rev, updated["revision"])
	}

	// The stale revision is now a conflict, not a silent overwrite.
	call(t, "PATCH", "/v1/stories/"+id, alice, body, ifMatch(rev)).expect(http.StatusConflict)
}

func TestStoryOwnershipIsEnforced(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Alice's")
	id := story["id"].(string)
	rev := int64(story["revision"].(float64))

	// A non-owner is refused on read and on both writes.
	//
	// These answer 403 rather than 404, which confirms the story exists to
	// someone who cannot read it. Story ids are UUIDs so this is not
	// enumerable, and the codes are asserted as-is rather than changed
	// underneath callers — but 404 would leak less.
	get(t, "/v1/stories/"+id, bob).expect(http.StatusForbidden)
	call(t, "PATCH", "/v1/stories/"+id, bob,
		map[string]any{"title": "Stolen", "tags": []string{}}, ifMatch(rev)).
		expect(http.StatusForbidden)
	call(t, "DELETE", "/v1/stories/"+id, bob, nil, ifMatch(rev)).expect(http.StatusForbidden)

	still := get(t, "/v1/stories/"+id, alice).expect(http.StatusOK).json()
	if still["title"] != "Alice's" {
		t.Errorf("story was modified by a non-owner: %v", still["title"])
	}
}

func TestStoryDelete(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Doomed")
	id := story["id"].(string)
	rev := int64(story["revision"].(float64))

	call(t, "DELETE", "/v1/stories/"+id, alice, nil, ifMatch(rev)).expect(http.StatusNoContent)
	get(t, "/v1/stories/"+id, alice).expect(http.StatusNotFound)

	// Chapters go with it (foreign key cascade).
	var chapters int
	if err := testPool.QueryRow(context.Background(),
		"SELECT count(*) FROM chapters WHERE story_id=$1", id).Scan(&chapters); err != nil {
		t.Fatal(err)
	}
	if chapters != 0 {
		t.Errorf("expected chapters to cascade, %d remain", chapters)
	}
}

func TestStoryLimitIsEnforced(t *testing.T) {
	reset(t)
	ctx := context.Background()
	// 100 rows straight into the table — going through the API 100 times just
	// to reach the ceiling would dominate the suite's runtime.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO stories (id, owner_id, title)
		SELECT gen_random_uuid(), $1, 'Filler ' || g FROM generate_series(1,100) g`, alice); err != nil {
		t.Fatal(err)
	}

	call(t, "POST", "/v1/stories", alice, map[string]any{"title": "One Too Many"}).
		expect(http.StatusUnprocessableEntity)

	// The ceiling is per user, so bob is unaffected.
	call(t, "POST", "/v1/stories", bob, map[string]any{"title": "Fine"}).expect(http.StatusCreated)
}

// ── chapters ────────────────────────────────────────────────────────────────

func TestChapterCRUD(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "With Chapters")
	id := story["id"].(string)
	base := "/v1/stories/" + id + "/chapters"

	created := call(t, "POST", base, alice, map[string]any{
		"title": "The Grafted Branch", "content": "<p>one two three</p>", "position": 1,
	}).expect(http.StatusCreated).json()

	chapterID := created["id"].(string)
	if created["wordCount"].(float64) != 3 {
		t.Errorf("wordCount = %v, want 3", created["wordCount"])
	}

	got := get(t, base+"/"+chapterID, alice).expect(http.StatusOK).json()
	if got["title"] != "The Grafted Branch" {
		t.Errorf("title = %v", got["title"])
	}

	rev := int64(created["revision"].(float64))
	updated := call(t, "PATCH", base+"/"+chapterID, alice, map[string]any{
		"title": "Renamed", "content": "<p>four</p>", "position": 1,
	}, ifMatch(rev)).expect(http.StatusOK).json()
	if updated["title"] != "Renamed" {
		t.Errorf("title = %v", updated["title"])
	}

	// Stale revision must conflict.
	call(t, "PATCH", base+"/"+chapterID, alice, map[string]any{
		"title": "Nope", "content": "x", "position": 1,
	}, ifMatch(rev)).expect(http.StatusConflict)

	newRev := int64(updated["revision"].(float64))
	call(t, "DELETE", base+"/"+chapterID, alice, nil, ifMatch(newRev)).expect(http.StatusNoContent)
	get(t, base+"/"+chapterID, alice).expect(http.StatusNotFound)
}

func TestChapterOrdering(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Ordered")
	base := "/v1/stories/" + story["id"].(string) + "/chapters"

	for _, p := range []float64{3, 1, 2} {
		call(t, "POST", base, alice, map[string]any{
			"title": fmt.Sprintf("Ch %g", p), "content": "x", "position": p,
		}).expect(http.StatusCreated)
	}

	chapters := get(t, base, alice).expect(http.StatusOK).list()
	var positions []float64
	for _, c := range chapters {
		positions = append(positions, c["position"].(float64))
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] < positions[i-1] {
			t.Fatalf("chapters not ordered by position: %v", positions)
		}
	}
}

func TestChapterPositionMustBeUnique(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Ordered")
	base := "/v1/stories/" + story["id"].(string) + "/chapters"

	call(t, "POST", base, alice, map[string]any{
		"title": "First", "content": "x", "position": 1,
	}).expect(http.StatusCreated)

	// chapters is UNIQUE (story_id, position). Reusing one is the caller's
	// mistake, so it conflicts rather than raising a constraint error the
	// client sees as a 500.
	call(t, "POST", base, alice, map[string]any{
		"title": "Also first", "content": "x", "position": 1,
	}).expect(http.StatusConflict)

	// The ceiling is per story, so the same position is fine elsewhere.
	other := newStory(t, alice, "Separate")
	call(t, "POST", "/v1/stories/"+other["id"].(string)+"/chapters", alice,
		map[string]any{"title": "First", "content": "x", "position": 1}).
		expect(http.StatusCreated)
}

func TestChapterWordLimit(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Wordy")
	base := "/v1/stories/" + story["id"].(string) + "/chapters"

	long := strings.Repeat("word ", 5001)
	call(t, "POST", base, alice, map[string]any{
		"title": "Too long", "content": long, "position": 1,
	}).expect(http.StatusUnprocessableEntity)
}

func TestChapterNotFoundForUnknownIDs(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Real")
	base := "/v1/stories/" + story["id"].(string) + "/chapters"

	// A well-formed but absent id, and a malformed one, both 404 rather than 500.
	get(t, base+"/11111111-1111-1111-1111-111111111111", alice).expect(http.StatusNotFound)
	get(t, base+"/not-a-uuid", alice).expect(http.StatusNotFound)
	get(t, "/v1/stories/11111111-1111-1111-1111-111111111111/chapters", alice).
		expect(http.StatusNotFound)
}

func TestChapterWriteEnqueuesIndexingOutbox(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Indexed")
	id := story["id"].(string)

	call(t, "POST", "/v1/stories/"+id+"/chapters", alice, map[string]any{
		"title": "Indexed chapter", "content": "<p>body</p>", "position": 1,
	}).expect(http.StatusCreated)

	// The outbox is what feeds pgvector; a content write that skips it would
	// leave the story permanently unsearchable with no visible error.
	var events int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM indexing_outbox WHERE story_id=$1 AND aggregate_type='chapter'`, id).
		Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Error("chapter write did not enqueue an indexing_outbox event")
	}
}

// ?content=false backs the MCP chapter index: listing a book to read its running
// order must not transfer every chapter body.
func TestListChaptersCanOmitContent(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "The Salt Road")
	sid := story["id"].(string)
	call(t, "POST", "/v1/stories/"+sid+"/chapters", alice,
		map[string]any{"title": "One", "content": "<p>needle in the haystack</p>", "position": 1.0}).
		expect(http.StatusCreated)

	full := call(t, "GET", "/v1/stories/"+sid+"/chapters", alice, nil).expect(http.StatusOK)
	if !strings.Contains(string(full.Body), "needle") {
		t.Error("the default listing should still carry chapter content")
	}

	lean := call(t, "GET", "/v1/stories/"+sid+"/chapters?content=false", alice, nil).
		expect(http.StatusOK)
	if strings.Contains(string(lean.Body), "needle") {
		t.Error("content=false must not return chapter bodies")
	}
	// Creating a story also creates "Chapter 1", so the added chapter is the
	// second row in position order.
	rows := lean.list()
	var added map[string]any
	for _, row := range rows {
		if row["title"] == "One" {
			added = row
		}
		if row["content"] != "" {
			t.Errorf("content = %q for %v, want empty", row["content"], row["title"])
		}
	}
	if added == nil {
		t.Fatalf("expected the added chapter in the index, got %s", lean.Body)
	}
	if added["wordCount"] == nil || added["revision"] == nil {
		t.Error("metadata the index is for (wordCount, revision) must survive")
	}
	if len(rows) != len(full.list()) {
		t.Error("the index must list the same chapters as the full listing")
	}
}
