package e2e

// Public reads: the anonymous discovery surface.
//
// The whole point of this domain is what it refuses to show. Every test here
// is really asking one of two questions: does an unpublished story leak, and
// does a listing leak chapter prose it should not.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// views drives discovery ranking and the endpoint takes no credential, so it
// counts readers per day rather than requests. It used to be one UPDATE per
// call: a shell loop could set any story's count to any number.
func TestViewsAreDeduplicated(t *testing.T) {
	reset(t)
	storyID := newPublishedStory(t, alice, "Watched")["id"].(string)
	views := func() float64 {
		story := get(t, publicStoryPath(storyID), "").expect(http.StatusOK).json()["story"].(map[string]any)
		return story["views"].(float64)
	}
	from := func(ip string) {
		call(t, "POST", publicStoryPath(storyID)+"/views", "", nil,
			map[string]string{"X-Forwarded-For": ip}).expect(http.StatusNoContent)
	}

	// The flood the review reproduced. Every call still succeeds — the reader
	// asked for their view to be recorded and it is — but the counter moves
	// once.
	for i := 0; i < 50; i++ {
		from("203.0.113.7")
	}
	if got := views(); got != 1 {
		t.Errorf("views after 50 requests from one reader = %v, want 1", got)
	}

	// A different reader is a different view.
	from("198.51.100.4")
	if got := views(); got != 2 {
		t.Errorf("views after a second reader = %v, want 2", got)
	}

	// A signed-in reader is keyed on their uid, so they count once however
	// their address moves around.
	for i := 0; i < 5; i++ {
		call(t, "POST", publicStoryPath(storyID)+"/views", bob, nil,
			map[string]string{"X-Forwarded-For": fmt.Sprintf("192.0.2.%d", i)}).
			expect(http.StatusNoContent)
	}
	if got := views(); got != 3 {
		t.Errorf("views after a signed-in reader = %v, want 3", got)
	}

	// Only the hash is stored; the table must never hold an address.
	var addresses int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM public_story_view_hits WHERE viewer_key LIKE '%203.0.113.7%'`).
		Scan(&addresses); err != nil {
		t.Fatal(err)
	}
	if addresses != 0 {
		t.Errorf("view rows hold %d raw client addresses", addresses)
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

func TestPublicListingFiltersByTagAndAuthor(t *testing.T) {
	reset(t)
	tagged := call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": "Tagged", "description": "d", "authorName": "a",
		"tags": []string{"Dark Fantasy", "x"}, "published": true,
	}).expect(http.StatusCreated).json()["id"]
	other := newPublishedStory(t, bob, "Untagged")["id"]
	// A draft carrying the tag must not surface through the filter.
	call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": "Draft", "description": "d", "authorName": "a",
		"tags": []string{"dark fantasy"},
	}).expect(http.StatusCreated)

	// The URL form is case-insensitive with spaces as hyphens.
	for _, q := range []string{"?tag=dark-fantasy", "?tag=Dark%20Fantasy"} {
		if hits := pageStories(t, publicPage(t, q)); len(hits) != 1 || hits[0]["id"] != tagged {
			t.Errorf("%s = %v", q, hits)
		}
	}
	if none := pageStories(t, publicPage(t, "?tag=dark")); len(none) != 0 {
		t.Errorf("a tag filter matched a partial tag: %v", none)
	}

	if hits := pageStories(t, publicPage(t, "?author="+bob)); len(hits) != 1 || hits[0]["id"] != other {
		t.Errorf("author filter = %v", hits)
	}
	if none := pageStories(t, publicPage(t, "?author=nobody")); len(none) != 0 {
		t.Errorf("an unknown author returned stories: %v", none)
	}
}

func TestPublicSitemapListsOnlyPublishedStories(t *testing.T) {
	reset(t)
	first := newPublishedStory(t, alice, "First Out")["id"].(string)
	second := newPublishedStory(t, bob, "Second Out")["id"].(string)
	newStory(t, alice, "Never Out")

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 3; pages++ {
		res := get(t, "/v1/public/sitemap?limit=1"+cursor, "").expect(http.StatusOK)
		page := res.json()
		for _, raw := range page["stories"].([]any) {
			entry := raw.(map[string]any)
			seen[entry["id"].(string)] = true
			if entry["title"] == "" || entry["authorId"] == "" || entry["updatedAt"] == "" {
				t.Errorf("sitemap entry is missing a field: %v", entry)
			}
		}
		next, _ := page["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = "&cursor=" + next
	}
	if len(seen) != 2 || !seen[first] || !seen[second] {
		t.Errorf("sitemap = %v, want exactly the two published stories", seen)
	}

	// An empty catalogue is an empty array, never null.
	reset(t)
	empty := get(t, "/v1/public/sitemap", "").expect(http.StatusOK).json()
	if stories, ok := empty["stories"].([]any); !ok || len(stories) != 0 {
		t.Errorf("empty sitemap = %v", empty["stories"])
	}
}

func TestPublicListingSearchesTitleAndAuthor(t *testing.T) {
	reset(t)
	call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": "The Dragon's Egg", "description": "d", "authorName": "Ada Bell",
		"tags": []string{"x"}, "published": true, "category": "fantasy",
	}).expect(http.StatusCreated)
	call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": "Quiet Harbour", "description": "d", "authorName": "Cyd Rowe",
		"tags": []string{"x"}, "published": true, "category": "fantasy",
	}).expect(http.StatusCreated)

	// Matches mid-title, not just as a prefix.
	if hits := pageStories(t, publicPage(t, "?q=dragon")); len(hits) != 1 ||
		hits[0]["title"] != "The Dragon's Egg" {
		t.Errorf("title search = %v", hits)
	}
	// The author name is searchable through the same term.
	if hits := pageStories(t, publicPage(t, "?q=rowe")); len(hits) != 1 ||
		hits[0]["title"] != "Quiet Harbour" {
		t.Errorf("author search = %v", hits)
	}
	// Search composes with the category filter rather than replacing it.
	if hits := pageStories(t, publicPage(t, "?q=dragon&category=horror")); len(hits) != 0 {
		t.Errorf("expected no matches, got %v", hits)
	}
	// No match is an empty page, not an error.
	if hits := pageStories(t, publicPage(t, "?q=zzzznope")); len(hits) != 0 {
		t.Errorf("expected no matches, got %v", hits)
	}
	// LIKE wildcards in the term are literal characters, not patterns — "%"
	// must not turn the search into "match everything".
	if hits := pageStories(t, publicPage(t, "?q=%25")); len(hits) != 0 {
		t.Errorf("wildcard leaked into the pattern: %v", hits)
	}
	// An overlong term is rejected before it reaches the database.
	get(t, "/v1/public/stories?q="+strings.Repeat("a", 101), "").
		expect(http.StatusBadRequest)
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

func TestPublicStoryShowsTheAuthorsCurrentUsername(t *testing.T) {
	reset(t)
	id := newPublishedStory(t, alice, "Whose Name Is This")["id"].(string)

	if got := pageStories(t, publicPage(t, ""))[0]["authorName"]; got != "a" {
		t.Fatalf("without a profile, authorName = %v, want the stored name", got)
	}

	putProfile(t, alice, map[string]any{"username": "alice_first"}).expect(http.StatusCreated)
	if got := pageStories(t, publicPage(t, ""))[0]["authorName"]; got != "alice_first" {
		t.Fatalf("listing authorName = %v, want alice_first", got)
	}

	call(t, "PATCH", "/v1/profiles/me", alice, map[string]any{"username": "alice_later"}).
		expect(http.StatusOK)
	if got := pageStories(t, publicPage(t, ""))[0]["authorName"]; got != "alice_later" {
		t.Fatalf("listing authorName after rename = %v, want alice_later", got)
	}
	detail := get(t, publicStoryPath(id), "").expect(http.StatusOK).json()
	if got := detail["story"].(map[string]any)["authorName"]; got != "alice_later" {
		t.Fatalf("detail authorName after rename = %v, want alice_later", got)
	}
}

func TestPublicReadsAreCacheableOnlyBySharedCaches(t *testing.T) {
	reset(t)
	id := newPublishedStory(t, alice, "Cached Somewhere")["id"].(string)
	chapterID := newChapter(t, alice, id, "Chapter One", 1)["id"].(string)

	listCache := "public, max-age=0, s-maxage=30, stale-while-revalidate=300"
	detailCache := "public, max-age=0, s-maxage=30"
	chapterCache := "public, max-age=0, s-maxage=60, stale-while-revalidate=600"
	for path, want := range map[string]string{
		"/v1/public/stories":                           listCache,
		publicStoryPath(id):                            detailCache,
		publicStoryPath(id) + "/chapters/" + chapterID: chapterCache,
	} {
		res := get(t, path, "").expect(http.StatusOK)
		if got := res.Header.Get("Cache-Control"); got != want {
			t.Errorf("%s Cache-Control = %q, want %q", path, got, want)
		}
		if etag := res.Header.Get("ETag"); !strings.HasPrefix(etag, `W/"`) {
			t.Errorf("%s ETag = %q, want a weak validator", path, etag)
		}
	}

	comments := get(t, publicStoryPath(id)+"/comments", "").expect(http.StatusOK)
	if got := comments.Header.Get("Cache-Control"); got != "" {
		t.Errorf("comments vary by viewer but carry Cache-Control %q", got)
	}
	missing := get(t, publicStoryPath("11111111-1111-1111-1111-111111111111"), "").
		expect(http.StatusNotFound)
	if missing.Header.Get("Cache-Control") != "" || missing.Header.Get("ETag") != "" {
		t.Errorf("a 404 was made cacheable: %v", missing.Header)
	}
}

func TestPublicChapterRevalidatesWithETag(t *testing.T) {
	reset(t)
	id := newPublishedStory(t, alice, "Revalidated")["id"].(string)
	chapter := newChapter(t, alice, id, "Chapter One", 1)
	path := publicStoryPath(id) + "/chapters/" + chapter["id"].(string)

	first := get(t, path, "").expect(http.StatusOK)
	etag := first.Header.Get("ETag")

	unchanged := get(t, path, "", map[string]string{"If-None-Match": etag}).
		expect(http.StatusNotModified)
	if len(unchanged.Body) != 0 {
		t.Errorf("304 carried a body: %q", unchanged.Body)
	}
	if unchanged.Header.Get("ETag") != etag {
		t.Errorf("304 ETag = %q, want %q", unchanged.Header.Get("ETag"), etag)
	}

	call(t, "PATCH", "/v1/stories/"+id+"/chapters/"+chapter["id"].(string), alice,
		map[string]any{"title": "Chapter One", "content": "<p>rewritten</p>", "position": 1},
		ifMatch(rev(t, chapter))).expect(http.StatusOK)

	changed := get(t, path, "", map[string]string{"If-None-Match": etag}).
		expect(http.StatusOK)
	if changed.Header.Get("ETag") == etag {
		t.Error("editing the chapter did not change its ETag")
	}
	if !strings.Contains(string(changed.Body), "rewritten") {
		t.Errorf("revalidated body is stale: %s", changed.Body)
	}
}

func TestPublicStoryDetailCarriesAuthorProfile(t *testing.T) {
	reset(t)
	id := newPublishedStory(t, alice, "With An Author")["id"].(string)

	bare := get(t, publicStoryPath(id), "").expect(http.StatusOK).json()
	if author, ok := bare["author"].(map[string]any); !ok || len(author) != 0 {
		t.Fatalf("author without a profile = %v, want an empty object", bare["author"])
	}

	wallet := "0x" + strings.Repeat("b", 40)
	putProfile(t, alice, map[string]any{
		"username": "alice_writes", "bio": "Writes about the sea.",
		"photoUrl": "https://example.test/alice.png", "walletAddress": wallet,
	}).expect(http.StatusCreated)

	detail := get(t, publicStoryPath(id), "").expect(http.StatusOK).json()
	want := map[string]any{
		"bio": "Writes about the sea.", "photoUrl": "https://example.test/alice.png", "walletAddress": wallet,
	}
	author := detail["author"].(map[string]any)
	for k, v := range want {
		if author[k] != v {
			t.Errorf("author[%q] = %v, want %v", k, author[k], v)
		}
	}
	if got := detail["story"].(map[string]any)["authorName"]; got != "alice_writes" {
		t.Errorf("authorName = %v, want alice_writes", got)
	}

	listed := get(t, "/v1/public/stories", "").expect(http.StatusOK).json()
	if _, ok := listed["stories"].([]any)[0].(map[string]any)["bio"]; ok {
		t.Error("the list should not carry author profile fields")
	}
}
