package httpapi_test

// Public reads: the anonymous discovery surface.
//
// The whole point of this domain is what it refuses to show. Every test here
// is really asking one of two questions: does an unpublished story leak, and
// does a listing leak chapter prose it should not.

import (
	"fmt"
	"net/http"
	"testing"
)

func publicStoryPath(id string) string { return "/v1/public/stories/" + id }

// publicPage reads a page of the discovery listing as an anonymous caller.
func publicPage(t *testing.T, query string) map[string]any {
	t.Helper()
	return get(t, "/v1/public/stories"+query, "").expect(http.StatusOK).json()
}

func pageStories(t *testing.T, page map[string]any) []map[string]any {
	t.Helper()
	raw, ok := page["stories"]
	if !ok || raw == nil {
		t.Fatalf("stories missing or null in %v", page)
	}
	out := []map[string]any{}
	for _, s := range raw.([]any) {
		out = append(out, s.(map[string]any))
	}
	return out
}

// ── visibility ──────────────────────────────────────────────────────────────

func TestPublicOnlyShowsPublishedStories(t *testing.T) {
	reset(t)
	published := newPublishedStory(t, alice, "Out In The World")["id"].(string)
	draft := newStory(t, alice, "Still A Draft")["id"].(string)

	listed := pageStories(t, publicPage(t, ""))
	if len(listed) != 1 || listed[0]["id"] != published {
		t.Fatalf("listing should hold only the published story, got %v", listed)
	}

	get(t, publicStoryPath(published), "").expect(http.StatusOK)
	// The draft is invisible to everyone through this surface, including its
	// own author — the public reader has no notion of a caller.
	get(t, publicStoryPath(draft), "").expect(http.StatusNotFound)
	get(t, publicStoryPath(draft), alice).expect(http.StatusNotFound)

	// Unpublishing removes it from the listing.
	story := get(t, "/v1/stories/"+published, alice).expect(http.StatusOK).json()
	call(t, "PATCH", "/v1/stories/"+published, alice, map[string]any{
		"title": "Out In The World", "description": "d", "authorName": "a",
		"tags": []string{"x"}, "published": false,
	}, ifMatch(rev(t, story))).expect(http.StatusOK)

	if after := pageStories(t, publicPage(t, "")); len(after) != 0 {
		t.Errorf("an unpublished story is still listed: %v", after)
	}
	get(t, publicStoryPath(published), "").expect(http.StatusNotFound)
}

func TestPublicStoryDetailHidesChapterProse(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "With Chapters")["id"].(string)
	chapterID := newChapter(t, alice, storyID, "The Opening", 1)["id"].(string)

	detail := get(t, publicStoryPath(storyID), "").expect(http.StatusOK).json()
	chapters := detail["chapters"].([]any)
	if len(chapters) != 2 {
		t.Fatalf("expected the auto-created chapter plus one, got %d", len(chapters))
	}
	// The detail view is a table of contents: titles and word counts, no prose.
	for _, raw := range chapters {
		c := raw.(map[string]any)
		if content, ok := c["content"]; ok && content != "" {
			t.Errorf("the story detail leaked chapter content: %v", content)
		}
	}

	// Content only comes from the single-chapter read.
	one := get(t, publicStoryPath(storyID)+"/chapters/"+chapterID, "").
		expect(http.StatusOK).json()
	if one["content"] != "<p>body</p>" {
		t.Errorf("chapter content = %v", one["content"])
	}
	if one["wordCount"].(float64) != 1 {
		t.Errorf("wordCount = %v", one["wordCount"])
	}

	// A chapter of an unpublished story is not readable either.
	draft := newStory(t, alice, "Draft")["id"].(string)
	draftChapter := newChapter(t, alice, draft, "Hidden", 1)["id"].(string)
	get(t, publicStoryPath(draft)+"/chapters/"+draftChapter, "").
		expect(http.StatusNotFound)
}

func TestPublicStoryCarriesTagsAndSocialCounters(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "Counted")["id"].(string)
	call(t, "PUT", "/v1/stories/"+storyID+"/likes/me", bob, nil).expect(http.StatusOK)
	call(t, "POST", "/v1/stories/"+storyID+"/ratings", bob,
		map[string]any{"rating": 4}).expect(http.StatusCreated)

	story := get(t, publicStoryPath(storyID), "").expect(http.StatusOK).json()["story"].(map[string]any)
	if story["likeCount"].(float64) != 1 || story["ratingsCount"].(float64) != 1 {
		t.Errorf("counters = %v", story)
	}
	if story["averageRating"].(float64) != 4 {
		t.Errorf("averageRating = %v", story["averageRating"])
	}
	if tags := story["tags"].([]any); len(tags) != 1 || tags[0] != "x" {
		t.Errorf("tags = %v", tags)
	}
	// chapterCount counts the auto-created chapter.
	if story["chapterCount"].(float64) != 1 {
		t.Errorf("chapterCount = %v", story["chapterCount"])
	}
	// The owner id is exposed deliberately — it is the link to the author's
	// public profile — but nothing else about the account is.
	if story["authorId"] != alice {
		t.Errorf("authorId = %v", story["authorId"])
	}
}

// ── views ───────────────────────────────────────────────────────────────────

func TestPublicStoryViews(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "Watched")["id"].(string)

	for i := 0; i < 3; i++ {
		call(t, "POST", publicStoryPath(storyID)+"/views", "", nil).
			expect(http.StatusNoContent)
	}
	story := get(t, publicStoryPath(storyID), "").expect(http.StatusOK).json()["story"].(map[string]any)
	// Pinned as-is: the counter is unauthenticated and unthrottled, so it
	// measures requests rather than readers.
	if story["views"].(float64) != 3 {
		t.Errorf("views = %v, want 3", story["views"])
	}

	draft := newStory(t, alice, "Unwatched")["id"].(string)
	call(t, "POST", publicStoryPath(draft)+"/views", "", nil).expect(http.StatusNotFound)
}

// ── pagination and filtering ────────────────────────────────────────────────

func TestPublicListingPaginatesWithoutRepeats(t *testing.T) {
	reset(t)
	const total = 5
	for i := 0; i < total; i++ {
		newPublishedStory(t, alice, fmt.Sprintf("Story %d", i))
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		url := "?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		page := publicPage(t, url)
		stories := pageStories(t, page)
		if len(stories) > 2 {
			t.Fatalf("page returned %d stories, limit was 2", len(stories))
		}
		for _, s := range stories {
			id := s["id"].(string)
			if seen[id] {
				t.Errorf("story %s appeared on two pages", id)
			}
			seen[id] = true
		}
		next, ok := page["nextCursor"]
		if !ok || next == nil || next == "" {
			break
		}
		cursor = next.(string)
	}
	if len(seen) != total {
		t.Errorf("pagination covered %d of %d stories", len(seen), total)
	}

	// A malformed cursor is bad input, reported through the same sentinel as
	// every other validation failure — the guestbook's cursor answers 422 too.
	get(t, "/v1/public/stories?cursor=not-base64!!", "").
		expect(http.StatusUnprocessableEntity)
	get(t, "/v1/public/stories?cursor="+
		"eyJub3RBQ3Vyc29yIjoxfQ", "").expect(http.StatusUnprocessableEntity)
	// A nonsense limit is rejected by the handler before the store sees it.
	get(t, "/v1/public/stories?limit=0", "").expect(http.StatusBadRequest)
	get(t, "/v1/public/stories?limit=abc", "").expect(http.StatusBadRequest)
}

func TestPublicListingFiltersByCategory(t *testing.T) {
	reset(t)
	call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": "Fantasy One", "description": "d", "authorName": "a",
		"tags": []string{"x"}, "published": true, "category": "fantasy",
	}).expect(http.StatusCreated)
	call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": "Horror One", "description": "d", "authorName": "a",
		"tags": []string{"x"}, "published": true, "category": "horror",
	}).expect(http.StatusCreated)

	if all := pageStories(t, publicPage(t, "")); len(all) != 2 {
		t.Fatalf("expected 2 stories, got %d", len(all))
	}
	fantasy := pageStories(t, publicPage(t, "?category=fantasy"))
	if len(fantasy) != 1 || fantasy[0]["title"] != "Fantasy One" {
		t.Errorf("category filter = %v", fantasy)
	}
	// An unknown category is an empty page, not an error.
	if none := pageStories(t, publicPage(t, "?category=nonexistent")); len(none) != 0 {
		t.Errorf("expected no matches, got %v", none)
	}
}

// ── ids ─────────────────────────────────────────────────────────────────────

func TestPublicRejectsMalformedIDs(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "Real")["id"].(string)
	absent := "11111111-1111-1111-1111-111111111111"

	get(t, publicStoryPath("not-a-uuid"), "").expect(http.StatusNotFound)
	get(t, publicStoryPath(absent), "").expect(http.StatusNotFound)
	get(t, publicStoryPath(storyID)+"/chapters/not-a-uuid", "").expect(http.StatusNotFound)
	get(t, publicStoryPath(storyID)+"/chapters/"+absent, "").expect(http.StatusNotFound)
	call(t, "POST", publicStoryPath("not-a-uuid")+"/views", "", nil).
		expect(http.StatusNotFound)

	// An empty listing serializes its collection as [], never null.
	reset(t)
	if s := pageStories(t, publicPage(t, "")); len(s) != 0 {
		t.Errorf("expected an empty listing, got %v", s)
	}
}
