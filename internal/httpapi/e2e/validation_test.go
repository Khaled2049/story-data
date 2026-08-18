package e2e

// Field validation: what the system of record refuses to store.
//
// Two classes of gap sat behind these tests. URL-shaped fields accepted any
// string, so a javascript: URI could be stored and then served back through
// the anonymous discovery endpoints — whether that executes depends on the
// client, but a column the schema calls a URL should not hold an executable
// URI either way. And most text fields had no ceiling at all, so the only
// bound on a write was the 1 MB body cap.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// nonHTTPURLs is the class the allowlist rejects: scheme-based execution,
// inline documents, and the whitespace trick that makes a parser and a browser
// disagree about where the scheme starts.
var nonHTTPURLs = []string{
	"javascript:alert(1)",
	"JavaScript:alert(1)",
	"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
	"vbscript:msgbox(1)",
	" javascript:alert(1)",
	"\tjavascript:alert(1)",
	"file:///etc/passwd",
	"//evil.example/x",
	"not a url at all",
}

func TestRejectsNonHTTPImageURLs(t *testing.T) {
	reset(t)
	storyID := newStory(t, alice, "Host")["id"].(string)

	for _, bad := range nonHTTPURLs {
		call(t, "POST", "/v1/stories", alice, map[string]any{
			"title": "Poisoned", "description": "d", "authorName": "a",
			"tags": []string{"x"}, "coverImageUrl": bad,
		}).expect(http.StatusUnprocessableEntity)

		call(t, "POST", "/v1/stories", alice, map[string]any{
			"title": "Poisoned", "description": "d", "authorName": "a",
			"tags": []string{"x"}, "thumbnailUrl": bad,
		}).expect(http.StatusUnprocessableEntity)

		call(t, "PUT", "/v1/profiles/me", bob, map[string]any{
			"username": "bob_v", "photoUrl": bad,
		}).expect(http.StatusUnprocessableEntity)

		call(t, "POST", worldPath(storyID, "characters"), alice, map[string]any{
			"name": "Ada", "artUrl": bad,
		}).expect(http.StatusUnprocessableEntity)

		call(t, "POST", worldPath(storyID, "places"), alice, map[string]any{
			"name": "Keep", "imageUrl": bad,
		}).expect(http.StatusUnprocessableEntity)

		call(t, "POST", "/v1/book-clubs", alice, map[string]any{
			"name": "Club", "description": "d", "image": bad,
			"category": "c", "activity": "a", "meetUp": "",
		}).expect(http.StatusUnprocessableEntity)
	}

	// Ordinary URLs and the empty string still work — every one of these
	// fields is optional.
	for _, good := range []string{"", "https://cdn.example.test/a.png", "http://example.test/b.jpg?x=1#y"} {
		created := call(t, "POST", "/v1/stories", alice, map[string]any{
			"title": "Fine", "description": "d", "authorName": "a",
			"tags": []string{"x"}, "coverImageUrl": good,
		}).expect(http.StatusCreated).json()
		if created["coverImageUrl"] != good {
			t.Errorf("coverImageUrl round-tripped as %v, want %q", created["coverImageUrl"], good)
		}
	}

	// The update path is guarded too — a ceiling that only guards creation is
	// not a ceiling.
	story := call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": "Editable", "description": "d", "authorName": "a", "tags": []string{"x"},
	}).expect(http.StatusCreated).json()
	call(t, "PATCH", "/v1/stories/"+story["id"].(string), alice, map[string]any{
		"title": "Editable", "description": "d", "authorName": "a",
		"tags": []string{"x"}, "coverImageUrl": "javascript:alert(1)",
	}, ifMatch(int64(story["revision"].(float64)))).expect(http.StatusUnprocessableEntity)
}

// The audit's PoC stored a 200 KB description, 500 tags and 120 characters in
// one story. Each of those is now a 422 rather than a row.
func TestFieldCeilings(t *testing.T) {
	reset(t)
	huge := strings.Repeat("x", 5001)
	tags := make([]string, 21)
	for i := range tags {
		tags[i] = fmt.Sprintf("tag%d", i)
	}

	for name, body := range map[string]map[string]any{
		"description": {"title": "T", "description": huge, "authorName": "a", "tags": []string{"x"}},
		"authorName":  {"title": "T", "description": "d", "authorName": strings.Repeat("a", 201), "tags": []string{"x"}},
		"category":    {"title": "T", "description": "d", "authorName": "a", "category": strings.Repeat("c", 101), "tags": []string{"x"}},
		"title":       {"title": strings.Repeat("t", 501), "description": "d", "authorName": "a", "tags": []string{"x"}},
		"tag count":   {"title": "T", "description": "d", "authorName": "a", "tags": tags},
		"tag length":  {"title": "T", "description": "d", "authorName": "a", "tags": []string{strings.Repeat("g", 101)}},
	} {
		if got := call(t, "POST", "/v1/stories", alice, body); got.Status != http.StatusUnprocessableEntity {
			t.Errorf("oversize %s = %d, want 422 — body: %s", name, got.Status, got.Body)
		}
	}

	// Nothing was stored on the way to those rejections.
	if n := len(get(t, "/v1/stories", alice).expect(http.StatusOK).list()); n != 0 {
		t.Errorf("%d stories survived a rejected create", n)
	}

	// The limits are counted in runes, not bytes: a title of 500 CJK
	// characters is 1500 bytes and must still be accepted.
	call(t, "POST", "/v1/stories", alice, map[string]any{
		"title": strings.Repeat("字", 500), "description": "d", "authorName": "a",
		"tags": []string{"x"},
	}).expect(http.StatusCreated)
}

func TestCompetitionAndClubFieldCeilings(t *testing.T) {
	reset(t)
	grantInitial(t, alice)
	huge := strings.Repeat("x", 5001)

	call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
		"title": "T", "description": huge, "category": "flash-fiction",
		"tags": []string{"x"}, "creatorName": "Alice",
	}).expect(http.StatusUnprocessableEntity)

	tags := make([]string, 21)
	for i := range tags {
		tags[i] = "t"
	}
	call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
		"title": "T", "description": "d", "category": "flash-fiction",
		"tags": tags, "creatorName": "Alice",
	}).expect(http.StatusUnprocessableEntity)

	call(t, "POST", "/v1/book-clubs", alice, map[string]any{
		"name": "Club", "description": huge, "image": "",
		"category": "c", "activity": "a", "meetUp": "",
	}).expect(http.StatusUnprocessableEntity)

	// A settings blob is stored verbatim and handed to every member, so it is
	// bounded as well as parsed.
	club := newClub(t, alice, "Bounded")["id"].(string)
	call(t, "PATCH", clubPath(club)+"/settings", alice, map[string]any{
		"bookOfTheMonth": map[string]any{"title": strings.Repeat("b", 17000)},
	}).expect(http.StatusUnprocessableEntity)
}

// A story with two hundred characters is not being written, it is being used
// as storage. The ceiling sits alongside the existing chapter and story ones.
func TestWorldbuildingEntityCeiling(t *testing.T) {
	reset(t)
	storyID := newStory(t, alice, "Crowded")["id"].(string)

	// Seeded directly: two hundred round trips would pay a lot of test time
	// for the same assertion, and the ceiling is read from the table.
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO characters (id, story_id, name)
		SELECT gen_random_uuid(), $1::uuid, 'filler ' || i FROM generate_series(1, 200) AS i`,
		storyID); err != nil {
		t.Fatal(err)
	}

	call(t, "POST", worldPath(storyID, "characters"), alice,
		map[string]any{"name": "One too many"}).expect(http.StatusUnprocessableEntity)

	// Editing what is already there still works — the ceiling is on creation.
	existing := get(t, worldPath(storyID, "characters"), alice).expect(http.StatusOK).list()[0]
	call(t, "PATCH", worldPath(storyID, "characters")+"/"+existing["id"].(string), alice,
		map[string]any{"name": "Renamed"},
		ifMatch(int64(existing["revision"].(float64)))).expect(http.StatusOK)

	// A different story is unaffected.
	other := newStory(t, alice, "Roomy")["id"].(string)
	newCharacter(t, alice, other, "Ada")
}
