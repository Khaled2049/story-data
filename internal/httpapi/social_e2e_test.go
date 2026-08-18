package httpapi_test

// Social: story likes, ratings, chapter comments and comment likes.
//
// Everything here hangs off a *published* story — `publicStoryExists` and
// `publicChapterExists` gate every method — so the fixtures publish first and
// the visibility tests lean on that. Comment threading is returned flat with a
// `parentId`, so the tests assert the parent link rather than any nesting.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// newPublishedStory creates a story that is visible to the public reader, which
// is the precondition for every social endpoint.
func newPublishedStory(t *testing.T, uid, title string) map[string]any {
	t.Helper()
	return call(t, "POST", "/v1/stories", uid, map[string]any{
		"title": title, "description": "d", "authorName": "a",
		"tags": []string{"x"}, "published": true,
	}).expect(http.StatusCreated).json()
}

// newChapter adds a chapter at an explicit position — chapters are
// UNIQUE (story_id, position), and creating a story already occupies one.
func newChapter(t *testing.T, uid, storyID, title string, position float64) map[string]any {
	t.Helper()
	return call(t, "POST", "/v1/stories/"+storyID+"/chapters", uid, map[string]any{
		"title": title, "content": "<p>body</p>", "position": position,
	}).expect(http.StatusCreated).json()
}

// socialFixture is the shape every test here needs: a published story with one
// chapter, owned by alice.
func socialFixture(t *testing.T) (storyID, chapterID string) {
	t.Helper()
	story := newPublishedStory(t, alice, "A Published Work")
	storyID = story["id"].(string)
	return storyID, newChapter(t, alice, storyID, "Chapter One", 1)["id"].(string)
}

func commentsPath(storyID, chapterID string) string {
	return "/v1/stories/" + storyID + "/chapters/" + chapterID + "/comments"
}
func publicCommentsPath(storyID, chapterID string) string {
	return "/v1/public/stories/" + storyID + "/chapters/" + chapterID + "/comments"
}

func newComment(t *testing.T, uid, storyID, chapterID, message, parentID string) map[string]any {
	t.Helper()
	body := map[string]any{"message": message}
	if parentID != "" {
		body["parentId"] = parentID
	}
	return call(t, "POST", commentsPath(storyID, chapterID), uid, body).
		expect(http.StatusCreated).json()
}

// ── story likes ─────────────────────────────────────────────────────────────

func TestSocialStoryLikes(t *testing.T) {
	reset(t)
	storyID, _ := socialFixture(t)
	likes := "/v1/stories/" + storyID + "/likes/me"

	liked := call(t, "PUT", likes, bob, nil).expect(http.StatusOK).json()
	if liked["likeCount"].(float64) != 1 {
		t.Errorf("likeCount = %v, want 1", liked["likeCount"])
	}
	// Liking twice is idempotent rather than a conflict.
	again := call(t, "PUT", likes, bob, nil).expect(http.StatusOK).json()
	if again["likeCount"].(float64) != 1 {
		t.Errorf("a second like counted twice: %v", again["likeCount"])
	}

	call(t, "PUT", likes, carol, nil).expect(http.StatusOK)
	me := get(t, "/v1/stories/"+storyID+"/social/me", bob).expect(http.StatusOK).json()
	if me["liked"] != true {
		t.Errorf("bob should read back as having liked: %v", me)
	}
	if seen := get(t, "/v1/stories/"+storyID+"/social/me", dave).expect(http.StatusOK).json(); seen["liked"] != false {
		t.Errorf("dave has not liked but reads %v", seen)
	}

	unliked := call(t, "DELETE", likes, bob, nil).expect(http.StatusOK).json()
	if unliked["likeCount"].(float64) != 1 {
		t.Errorf("after bob unliked, likeCount = %v, want 1 (carol)", unliked["likeCount"])
	}
	// Unliking twice is also idempotent.
	call(t, "DELETE", likes, bob, nil).expect(http.StatusOK)

	// Nothing here is public — a like needs a caller.
	call(t, "PUT", likes, "", nil).expect(http.StatusUnauthorized)
	get(t, "/v1/stories/"+storyID+"/social/me", "").expect(http.StatusUnauthorized)
}

func TestSocialUnpublishedStoryIsNotSociallyReachable(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Still A Draft")
	id := story["id"].(string)
	chapter := newChapter(t, alice, id, "Draft chapter", 1)["id"].(string)

	// Every social endpoint gates on the story being published — including for
	// the owner, who has no way to like their own unpublished draft.
	call(t, "PUT", "/v1/stories/"+id+"/likes/me", bob, nil).expect(http.StatusNotFound)
	call(t, "POST", "/v1/stories/"+id+"/ratings", bob,
		map[string]any{"rating": 5}).expect(http.StatusNotFound)
	get(t, "/v1/stories/"+id+"/social/me", alice).expect(http.StatusNotFound)
	call(t, "POST", commentsPath(id, chapter), alice,
		map[string]any{"message": "hi"}).expect(http.StatusNotFound)
	get(t, publicCommentsPath(id, chapter), "").expect(http.StatusNotFound)
}

// ── ratings ─────────────────────────────────────────────────────────────────

func TestSocialRatings(t *testing.T) {
	reset(t)
	storyID, _ := socialFixture(t)
	ratings := "/v1/stories/" + storyID + "/ratings"

	first := call(t, "POST", ratings, bob, map[string]any{"rating": 4}).
		expect(http.StatusCreated).json()
	if first["ratingsCount"].(float64) != 1 || first["averageRating"].(float64) != 4 {
		t.Errorf("summary after one rating = %v", first)
	}

	second := call(t, "POST", ratings, carol, map[string]any{"rating": 5}).
		expect(http.StatusCreated).json()
	// Averaged and rounded to one decimal in SQL.
	if second["averageRating"].(float64) != 4.5 || second["ratingsCount"].(float64) != 2 {
		t.Errorf("summary after two ratings = %v", second)
	}

	me := get(t, "/v1/stories/"+storyID+"/social/me", bob).expect(http.StatusOK).json()
	if me["rating"].(float64) != 4 {
		t.Errorf("bob's own rating = %v, want 4", me["rating"])
	}

	// Pinned as-is: a rating is permanently immutable. There is no PATCH or
	// DELETE route, and a second POST conflicts rather than replacing — so a
	// misclicked star cannot be corrected through the API.
	call(t, "POST", ratings, bob, map[string]any{"rating": 1}).expect(http.StatusConflict)

	// Out of range on both sides, and zero, are bad input.
	for _, bad := range []int{0, 6, -1} {
		call(t, "POST", ratings, dave, map[string]any{"rating": bad}).
			expect(http.StatusUnprocessableEntity)
	}
	call(t, "POST", ratings, "", map[string]any{"rating": 3}).expect(http.StatusUnauthorized)

	// A story with no ratings omits averageRating rather than reporting zero.
	other := newPublishedStory(t, alice, "Unrated")["id"].(string)
	summary := get(t, "/v1/public/stories/"+other, "").expect(http.StatusOK).json()
	if v, ok := summary["averageRating"]; ok && v != nil {
		t.Errorf("an unrated story reported averageRating = %v", v)
	}
}

// ── comments ────────────────────────────────────────────────────────────────

func TestSocialCommentLifecycle(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)

	created := newComment(t, bob, storyID, chapterID, "  A first thought.  ", "")
	if created["message"] != "A first thought." {
		t.Errorf("message was not trimmed: %q", created["message"])
	}
	if created["userId"] != bob || created["chapterId"] != chapterID {
		t.Errorf("comment = %v", created)
	}
	if created["parentId"] != nil {
		t.Errorf("a top-level comment should have no parentId, got %v", created["parentId"])
	}
	if created["likeCount"].(float64) != 0 || created["likedByMe"] != false {
		t.Errorf("a new comment should have no likes: %v", created)
	}
	id := created["id"].(string)

	// The thread is public, including to an anonymous reader.
	listed := get(t, publicCommentsPath(storyID, chapterID), "").
		expect(http.StatusOK).list()
	if len(listed) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(listed))
	}
	if listed[0]["likedByMe"] != false {
		t.Errorf("an anonymous reader should never be likedByMe: %v", listed[0])
	}

	updated := call(t, "PATCH", commentsPath(storyID, chapterID)+"/"+id, bob,
		map[string]any{"message": "A revised thought."}).expect(http.StatusOK).json()
	if updated["message"] != "A revised thought." {
		t.Errorf("message = %v", updated["message"])
	}

	call(t, "DELETE", commentsPath(storyID, chapterID)+"/"+id, bob, nil).
		expect(http.StatusNoContent)
	if left := get(t, publicCommentsPath(storyID, chapterID), "").expect(http.StatusOK).list(); len(left) != 0 {
		t.Errorf("comment survived deletion: %v", left)
	}
}

func TestSocialCommentValidationAndAuth(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)
	base := commentsPath(storyID, chapterID)

	call(t, "POST", base, bob, map[string]any{"message": "   "}).
		expect(http.StatusUnprocessableEntity)
	call(t, "POST", base, bob, map[string]any{"message": strings.Repeat("x", 10001)}).
		expect(http.StatusUnprocessableEntity)
	call(t, "POST", base, "", map[string]any{"message": "anon"}).
		expect(http.StatusUnauthorized)

	comment := newComment(t, bob, storyID, chapterID, "Bob's", "")
	id := comment["id"].(string)

	// Editing and deleting are author-only — not the story's owner, not a
	// stranger. Both answer 403 rather than 404, so the caller can tell the
	// comment exists.
	call(t, "PATCH", base+"/"+id, carol, map[string]any{"message": "hijacked"}).
		expect(http.StatusForbidden)
	call(t, "PATCH", base+"/"+id, alice, map[string]any{"message": "my story"}).
		expect(http.StatusForbidden)
	call(t, "DELETE", base+"/"+id, carol, nil).expect(http.StatusForbidden)

	// Pinned as-is: the story's own author cannot remove a comment on their
	// work. The guestbook deliberately allows the wall owner to; this does not.
	call(t, "DELETE", base+"/"+id, alice, nil).expect(http.StatusForbidden)
}

func TestSocialCommentReplies(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)

	parent := newComment(t, bob, storyID, chapterID, "The parent.", "")
	parentID := parent["id"].(string)
	reply := newComment(t, carol, storyID, chapterID, "The reply.", parentID)
	if reply["parentId"] != parentID {
		t.Errorf("parentId = %v, want %v", reply["parentId"], parentID)
	}

	// Threads come back flat, in creation order, for the client to reassemble.
	listed := get(t, publicCommentsPath(storyID, chapterID), "").
		expect(http.StatusOK).list()
	if len(listed) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(listed))
	}

	// A parent from a different chapter is not a valid parent.
	otherChapter := newChapter(t, alice, storyID, "Chapter Two", 2)["id"].(string)
	call(t, "POST", commentsPath(storyID, otherChapter), carol,
		map[string]any{"message": "x", "parentId": parentID}).
		expect(http.StatusNotFound)
	// Nor is a parent that does not exist, or a malformed one.
	call(t, "POST", commentsPath(storyID, chapterID), carol,
		map[string]any{"message": "x", "parentId": "11111111-1111-1111-1111-111111111111"}).
		expect(http.StatusNotFound)
	call(t, "POST", commentsPath(storyID, chapterID), carol,
		map[string]any{"message": "x", "parentId": "not-a-uuid"}).
		expect(http.StatusNotFound)

	// Deleting a parent takes its replies with it, silently — the reply's
	// author is given no warning and the response carries no count.
	call(t, "DELETE", commentsPath(storyID, chapterID)+"/"+parentID, bob, nil).
		expect(http.StatusNoContent)
	after := get(t, publicCommentsPath(storyID, chapterID), "").
		expect(http.StatusOK).list()
	if len(after) != 0 {
		t.Errorf("expected the reply to cascade with its parent, got %v", after)
	}
}

// ── comment likes ───────────────────────────────────────────────────────────

func TestSocialCommentLikes(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)
	comment := newComment(t, bob, storyID, chapterID, "Like me.", "")
	likes := commentsPath(storyID, chapterID) + "/" + comment["id"].(string) + "/likes"

	liked := call(t, "PUT", likes, carol, nil).expect(http.StatusOK).json()
	if liked["likeCount"].(float64) != 1 || liked["likedByMe"] != true {
		t.Errorf("after carol liked: %v", liked)
	}
	// Idempotent in both directions, so a double-tap cannot conflict.
	same := call(t, "PUT", likes, carol, nil).expect(http.StatusOK).json()
	if same["likeCount"].(float64) != 1 {
		t.Errorf("a second like counted twice: %v", same["likeCount"])
	}

	call(t, "PUT", likes, dave, nil).expect(http.StatusOK)

	// likedByMe is per viewer; the count is shared.
	seen := get(t, publicCommentsPath(storyID, chapterID), carol).
		expect(http.StatusOK).list()[0]
	if seen["likeCount"].(float64) != 2 || seen["likedByMe"] != true {
		t.Errorf("carol's view = %v", seen)
	}
	if mine := get(t, publicCommentsPath(storyID, chapterID), bob).expect(http.StatusOK).list()[0]; mine["likedByMe"] != false {
		t.Errorf("bob has not liked but sees likedByMe true: %v", mine)
	}

	removed := call(t, "DELETE", likes, carol, nil).expect(http.StatusOK).json()
	if removed["likeCount"].(float64) != 1 || removed["likedByMe"] != false {
		t.Errorf("after carol unliked: %v", removed)
	}
	call(t, "DELETE", likes, carol, nil).expect(http.StatusOK)

	call(t, "PUT", likes, "", nil).expect(http.StatusUnauthorized)
	// A comment that does not exist is a 404, not a 500.
	call(t, "PUT", commentsPath(storyID, chapterID)+
		"/11111111-1111-1111-1111-111111111111/likes", carol, nil).
		expect(http.StatusNotFound)
}

// ── ids ─────────────────────────────────────────────────────────────────────

// Every id segment on the comment routes reaches a uuid column. Unguarded, a
// malformed one makes PostgreSQL raise 22P02, which surfaces as a 500 for what
// is really a bad URL.
func TestSocialRejectsMalformedIDs(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)
	comment := newComment(t, bob, storyID, chapterID, "Real.", "")["id"].(string)
	absent := "11111111-1111-1111-1111-111111111111"

	// Malformed chapter id, on all four comment routes.
	call(t, "POST", commentsPath(storyID, "not-a-uuid"), bob,
		map[string]any{"message": "x"}).expect(http.StatusNotFound)
	call(t, "PATCH", commentsPath(storyID, "not-a-uuid")+"/"+comment, bob,
		map[string]any{"message": "x"}).expect(http.StatusNotFound)
	call(t, "DELETE", commentsPath(storyID, "not-a-uuid")+"/"+comment, bob, nil).
		expect(http.StatusNotFound)
	call(t, "PUT", commentsPath(storyID, "not-a-uuid")+"/"+comment+"/likes", bob, nil).
		expect(http.StatusNotFound)

	// Malformed comment id, on the routes that take one.
	call(t, "PATCH", commentsPath(storyID, chapterID)+"/not-a-uuid", bob,
		map[string]any{"message": "x"}).expect(http.StatusNotFound)
	call(t, "DELETE", commentsPath(storyID, chapterID)+"/not-a-uuid", bob, nil).
		expect(http.StatusNotFound)
	call(t, "PUT", commentsPath(storyID, chapterID)+"/not-a-uuid/likes", bob, nil).
		expect(http.StatusNotFound)

	// Well-formed but absent ids are 404 too.
	call(t, "POST", commentsPath(storyID, absent), bob,
		map[string]any{"message": "x"}).expect(http.StatusNotFound)
	call(t, "PATCH", commentsPath(storyID, chapterID)+"/"+absent, bob,
		map[string]any{"message": "x"}).expect(http.StatusNotFound)
	call(t, "DELETE", commentsPath(storyID, chapterID)+"/"+absent, bob, nil).
		expect(http.StatusNotFound)

	// And on the story segment, plus the public read.
	call(t, "PUT", "/v1/stories/not-a-uuid/likes/me", bob, nil).expect(http.StatusNotFound)
	get(t, publicCommentsPath(storyID, "not-a-uuid"), "").expect(http.StatusNotFound)
	get(t, publicCommentsPath(absent, chapterID), "").expect(http.StatusNotFound)

	// Empty threads serialize as [], never null.
	empty := newChapter(t, alice, storyID, "Quiet", 3)["id"].(string)
	get(t, publicCommentsPath(storyID, empty), "").expect(http.StatusOK).list()
}

// ── author names ────────────────────────────────────────────────────────────

// Comments carry the author's current username, joined at read time like the
// guestbook does. Without it every row renders "unknown" until the client
// resolves each author with its own request.
func TestSocialCommentsCarryAuthorUsername(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)
	newProfile(t, bob, "bob_writes", "everyone")

	created := newComment(t, bob, storyID, chapterID, "With a name.", "")
	if created["authorUsername"] != "bob_writes" {
		t.Errorf("authorUsername on create = %v, want bob_writes", created["authorUsername"])
	}

	listed := get(t, publicCommentsPath(storyID, chapterID), "").
		expect(http.StatusOK).list()
	if listed[0]["authorUsername"] != "bob_writes" {
		t.Errorf("authorUsername in the thread = %v", listed[0]["authorUsername"])
	}

	// An author with no profile yet still round-trips, with an empty name
	// rather than a missing row.
	newComment(t, carol, storyID, chapterID, "No profile.", "")
	var found bool
	for _, c := range get(t, publicCommentsPath(storyID, chapterID), "").expect(http.StatusOK).list() {
		if c["userId"] == carol {
			found = true
			if c["authorUsername"] != "" && c["authorUsername"] != nil {
				t.Errorf("a profile-less author got %v", c["authorUsername"])
			}
		}
	}
	if !found {
		t.Error("the profile-less author's comment is missing from the thread")
	}
}

func TestSocialCommentCascadesWithItsChapter(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)
	newComment(t, bob, storyID, chapterID, "Doomed.", "")

	story := get(t, "/v1/stories/"+storyID, alice).expect(http.StatusOK).json()
	chapter := get(t, "/v1/stories/"+storyID+"/chapters/"+chapterID, alice).
		expect(http.StatusOK).json()
	_ = story
	call(t, "DELETE", "/v1/stories/"+storyID+"/chapters/"+chapterID, alice, nil,
		ifMatch(rev(t, chapter))).expect(http.StatusNoContent)

	var remaining int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM chapter_comments WHERE chapter_id=$1`, chapterID).
		Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("expected comments to cascade with the chapter, %d remain", remaining)
	}
}

// ── daily budgets ───────────────────────────────────────────────────────────

// Comments were unmetered: one account could post without limit on any
// chapter, which is the cheapest way to make a shared surface unusable. The
// budget is per user per day and lives in the database, so it survives a
// restart and holds across instances — the in-process rate limiter only
// bounds bursts.
func TestCommentRateLimit(t *testing.T) {
	reset(t)
	storyID, chapterID := socialFixture(t)

	for i := 0; i < 100; i++ {
		call(t, "POST", commentsPath(storyID, chapterID), bob,
			map[string]any{"message": fmt.Sprintf("comment %d", i)}).
			expect(http.StatusCreated)
	}
	call(t, "POST", commentsPath(storyID, chapterID), bob,
		map[string]any{"message": "one too many"}).
		expect(http.StatusTooManyRequests)

	// The budget is per account, not per chapter or per story.
	second := newChapter(t, alice, storyID, "Chapter Two", 2)["id"].(string)
	call(t, "POST", commentsPath(storyID, second), bob,
		map[string]any{"message": "different chapter"}).
		expect(http.StatusTooManyRequests)

	// And it is one user's budget, not everyone's.
	call(t, "POST", commentsPath(storyID, chapterID), carol,
		map[string]any{"message": "unaffected"}).expect(http.StatusCreated)

	// A rejected comment must not be stored.
	listed := get(t, publicCommentsPath(storyID, chapterID), "").
		expect(http.StatusOK).list()
	if len(listed) != 101 {
		t.Errorf("chapter holds %d comments, want 101", len(listed))
	}
}

func TestRatingRateLimit(t *testing.T) {
	reset(t)
	// A rating is one per story per user, so spending the budget means rating
	// many different stories.
	ids := make([]string, 0, 51)
	for i := 0; i < 51; i++ {
		ids = append(ids, newPublishedStory(t, alice, fmt.Sprintf("Work %d", i))["id"].(string))
	}
	for i := 0; i < 50; i++ {
		call(t, "POST", "/v1/stories/"+ids[i]+"/ratings", bob,
			map[string]any{"rating": 5}).expect(http.StatusCreated)
	}
	call(t, "POST", "/v1/stories/"+ids[50]+"/ratings", bob,
		map[string]any{"rating": 5}).expect(http.StatusTooManyRequests)

	// The refused rating left nothing behind.
	story := get(t, "/v1/public/stories/"+ids[50], "").expect(http.StatusOK).
		json()["story"].(map[string]any)
	if story["ratingsCount"].(float64) != 0 {
		t.Errorf("a rejected rating was recorded: %v", story["ratingsCount"])
	}
}
