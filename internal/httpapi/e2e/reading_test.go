package e2e

// Reading progress and history.
//
// Progress is keyed (user, story) and only exists for published stories, so a
// draft can never appear in anyone's history. The list view is the interesting
// read: it derives the reader's position within the book rather than storing
// it, which is what keeps it correct when chapters are reordered.

import (
	"fmt"
	"net/http"
	"testing"
)

func progressPath(storyID string) string { return "/v1/me/reading-progress/" + storyID }

// ── progress ────────────────────────────────────────────────────────────────

func TestReadingProgressRoundTrip(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "A Long Book")["id"].(string)
	chapterID := newChapter(t, alice, storyID, "Chapter One", 1)["id"].(string)

	// Nothing saved yet reads as the start of the book rather than a 404.
	fresh := get(t, progressPath(storyID), bob).expect(http.StatusOK).json()
	if fresh["scrollPercent"].(float64) != 0 || fresh["storyId"] != storyID {
		t.Errorf("unread story = %v", fresh)
	}

	saved := call(t, "PUT", progressPath(storyID), bob, map[string]any{
		"chapterId": chapterID, "scrollPercent": 0.42,
	}).expect(http.StatusOK).json()
	if saved["scrollPercent"].(float64) != 0.42 || saved["chapterId"] != chapterID {
		t.Errorf("saved = %v", saved)
	}

	read := get(t, progressPath(storyID), bob).expect(http.StatusOK).json()
	if read["scrollPercent"].(float64) != 0.42 {
		t.Errorf("read back = %v", read)
	}

	// Saving again replaces rather than appending — one row per reader per
	// story.
	call(t, "PUT", progressPath(storyID), bob, map[string]any{
		"chapterId": chapterID, "scrollPercent": 0.9,
	}).expect(http.StatusOK)
	if again := get(t, progressPath(storyID), bob).expect(http.StatusOK).json(); again["scrollPercent"].(float64) != 0.9 {
		t.Errorf("progress did not replace: %v", again)
	}

	// Progress is per reader.
	if theirs := get(t, progressPath(storyID), carol).expect(http.StatusOK).json(); theirs["scrollPercent"].(float64) != 0 {
		t.Errorf("carol sees bob's progress: %v", theirs)
	}

	get(t, progressPath(storyID), "").expect(http.StatusUnauthorized)
}

func TestReadingProgressValidation(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "Bounded")["id"].(string)
	chapterID := newChapter(t, alice, storyID, "Chapter One", 1)["id"].(string)
	absent := "11111111-1111-1111-1111-111111111111"

	// scrollPercent is a fraction, not a percentage.
	for _, bad := range []float64{-0.1, 1.1, 42} {
		call(t, "PUT", progressPath(storyID), bob, map[string]any{
			"chapterId": chapterID, "scrollPercent": bad,
		}).expect(http.StatusUnprocessableEntity)
	}
	// Both bounds are inclusive.
	for _, ok := range []float64{0, 1} {
		call(t, "PUT", progressPath(storyID), bob, map[string]any{
			"chapterId": chapterID, "scrollPercent": ok,
		}).expect(http.StatusOK)
	}

	// A malformed or missing chapter is bad input, not a server error.
	call(t, "PUT", progressPath(storyID), bob, map[string]any{
		"chapterId": "not-a-uuid", "scrollPercent": 0.5,
	}).expect(http.StatusUnprocessableEntity)
	call(t, "PUT", progressPath(storyID), bob, map[string]any{
		"chapterId": absent, "scrollPercent": 0.5,
	}).expect(http.StatusNotFound)

	// A chapter that belongs to a different story cannot be used to fake a
	// position in this one.
	other := newPublishedStory(t, alice, "Elsewhere")["id"].(string)
	elsewhere := newChapter(t, alice, other, "Not here", 1)["id"].(string)
	call(t, "PUT", progressPath(storyID), bob, map[string]any{
		"chapterId": elsewhere, "scrollPercent": 0.5,
	}).expect(http.StatusNotFound)

	// Malformed story id in the path.
	call(t, "PUT", progressPath("not-a-uuid"), bob, map[string]any{
		"chapterId": chapterID, "scrollPercent": 0.5,
	}).expect(http.StatusNotFound)
}

func TestReadingProgressRequiresAPublishedStory(t *testing.T) {
	reset(t)
	draft := newStory(t, alice, "Draft")["id"].(string)
	chapterID := newChapter(t, alice, draft, "Chapter One", 1)["id"].(string)

	// Even the author cannot record progress against their own unpublished
	// draft — progress is a reader-facing record.
	call(t, "PUT", progressPath(draft), alice, map[string]any{
		"chapterId": chapterID, "scrollPercent": 0.5,
	}).expect(http.StatusNotFound)
	get(t, progressPath(draft), alice).expect(http.StatusNotFound)
}

// ── history ─────────────────────────────────────────────────────────────────

func TestReadingHistoryDerivesPositionInTheBook(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "Three Chapters")["id"].(string)
	first := newChapter(t, alice, storyID, "One", 1)["id"].(string)
	second := newChapter(t, alice, storyID, "Two", 2)["id"].(string)
	newChapter(t, alice, storyID, "Three", 3)

	call(t, "PUT", progressPath(storyID), bob, map[string]any{
		"chapterId": second, "scrollPercent": 0.5,
	}).expect(http.StatusOK)

	history := get(t, "/v1/me/reading-history", bob).expect(http.StatusOK).list()
	if len(history) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(history))
	}
	entry := history[0]
	if entry["storyTitle"] != "Three Chapters" {
		t.Errorf("storyTitle = %v", entry["storyTitle"])
	}
	// The auto-created chapter counts too, so four in total.
	if entry["totalChapters"].(float64) != 4 {
		t.Errorf("totalChapters = %v, want 4", entry["totalChapters"])
	}
	// chapterIndex is derived from position rather than stored, so it stays
	// right when chapters are reordered around the saved one.
	if entry["chapterIndex"].(float64) != 2 {
		t.Errorf("chapterIndex = %v, want 2", entry["chapterIndex"])
	}

	// Moving back to the first chapter moves the derived index with it.
	call(t, "PUT", progressPath(storyID), bob, map[string]any{
		"chapterId": first, "scrollPercent": 0.1,
	}).expect(http.StatusOK)
	if idx := get(t, "/v1/me/reading-history", bob).expect(http.StatusOK).list()[0]["chapterIndex"].(float64); idx != 1 {
		t.Errorf("chapterIndex after moving back = %v, want 1", idx)
	}
}

func TestReadingHistoryIsPerReaderAndClearable(t *testing.T) {
	reset(t)
	first := newPublishedStory(t, alice, "First")["id"].(string)
	firstChapter := newChapter(t, alice, first, "One", 1)["id"].(string)
	second := newPublishedStory(t, alice, "Second")["id"].(string)
	secondChapter := newChapter(t, alice, second, "One", 1)["id"].(string)

	call(t, "PUT", progressPath(first), bob, map[string]any{
		"chapterId": firstChapter, "scrollPercent": 0.2,
	}).expect(http.StatusOK)
	call(t, "PUT", progressPath(second), bob, map[string]any{
		"chapterId": secondChapter, "scrollPercent": 0.3,
	}).expect(http.StatusOK)

	if mine := get(t, "/v1/me/reading-history", bob).expect(http.StatusOK).list(); len(mine) != 2 {
		t.Fatalf("bob's history = %d entries, want 2", len(mine))
	}
	// Another reader's history is their own.
	if theirs := get(t, "/v1/me/reading-history", carol).expect(http.StatusOK).list(); len(theirs) != 0 {
		t.Errorf("carol's history = %v, want empty", theirs)
	}

	call(t, "DELETE", "/v1/me/reading-history", bob, nil).expect(http.StatusNoContent)
	if after := get(t, "/v1/me/reading-history", bob).expect(http.StatusOK).list(); len(after) != 0 {
		t.Errorf("history survived clearing: %v", after)
	}
	// Clearing an empty history is not an error.
	call(t, "DELETE", "/v1/me/reading-history", bob, nil).expect(http.StatusNoContent)

	// Progress rows go with it, so the story reads as unread again.
	if fresh := get(t, progressPath(first), bob).expect(http.StatusOK).json(); fresh["scrollPercent"].(float64) != 0 {
		t.Errorf("progress survived the clear: %v", fresh)
	}
}

func TestReadingHistoryLimitAndUnpublishing(t *testing.T) {
	reset(t)
	stories := []string{}
	for i := 0; i < 3; i++ {
		id := newPublishedStory(t, alice, fmt.Sprintf("Book %d", i))["id"].(string)
		chapter := newChapter(t, alice, id, "One", 1)["id"].(string)
		call(t, "PUT", progressPath(id), bob, map[string]any{
			"chapterId": chapter, "scrollPercent": 0.5,
		}).expect(http.StatusOK)
		stories = append(stories, id)
	}

	if page := get(t, "/v1/me/reading-history?limit=2", bob).expect(http.StatusOK).list(); len(page) != 2 {
		t.Errorf("limit=2 returned %d entries", len(page))
	}
	get(t, "/v1/me/reading-history?limit=0", bob).expect(http.StatusBadRequest)
	get(t, "/v1/me/reading-history?limit=51", bob).expect(http.StatusBadRequest)
	get(t, "/v1/me/reading-history?limit=abc", bob).expect(http.StatusBadRequest)
	get(t, "/v1/me/reading-history", "").expect(http.StatusUnauthorized)

	// A story the author unpublishes drops out of every reader's history, even
	// though the progress row survives.
	story := get(t, "/v1/stories/"+stories[0], alice).expect(http.StatusOK).json()
	call(t, "PATCH", "/v1/stories/"+stories[0], alice, map[string]any{
		"title": "Book 0", "description": "d", "authorName": "a",
		"tags": []string{"x"}, "published": false,
	}, ifMatch(rev(t, story))).expect(http.StatusOK)

	if after := get(t, "/v1/me/reading-history", bob).expect(http.StatusOK).list(); len(after) != 2 {
		t.Errorf("an unpublished story is still in history: %d entries", len(after))
	}
}
