package e2e

// Guestbook: entries, threaded replies, votes, and the five-policy wall.
//
// The wall policy is the densest authorization rule left in the service, and
// it is enforced only in Go — `canPostGuestbook` reads the owner's policy and
// the follow graph. Nothing in the schema stops a write that skips it, so the
// matrix below is the only thing pinning it down.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

const erin = "user-erin"

func guestbookOf(owner string) string { return "/v1/guestbooks/" + owner + "/entries" }
func publicWallOf(o string) string    { return "/v1/public/guestbooks/" + o + "/entries" }
func followPath(target string) string { return "/v1/profiles/" + target + "/follow" }

// newProfile registers a public profile, which is what carries the wall policy.
func newProfile(t *testing.T, uid, username, policy string) {
	t.Helper()
	call(t, "PUT", "/v1/profiles/me", uid, map[string]any{
		"username": username, "guestbookPolicy": policy,
	}).expect(http.StatusCreated)
}

func follow(t *testing.T, follower, followed string) {
	t.Helper()
	call(t, "PUT", followPath(followed), follower, nil).expect(http.StatusNoContent)
}

// postEntry writes on owner's wall as author.
func postEntry(t *testing.T, author, owner, content string) map[string]any {
	t.Helper()
	return call(t, "POST", guestbookOf(owner), author,
		map[string]any{"content": content}).expect(http.StatusCreated).json()
}

func postReply(t *testing.T, author, owner, entryID, parentID, content string) map[string]any {
	t.Helper()
	body := map[string]any{"content": content}
	if parentID != "" {
		body["parentId"] = parentID
	}
	return call(t, "POST", guestbookOf(owner)+"/"+entryID+"/replies", author, body).
		expect(http.StatusCreated).json()
}

// wallEntries reads a wall as viewer ("" for anonymous).
func wallEntries(t *testing.T, owner, viewer string) []map[string]any {
	t.Helper()
	page := get(t, publicWallOf(owner), viewer).expect(http.StatusOK).json()
	raw, ok := page["entries"]
	if !ok || raw == nil {
		t.Fatalf("entries missing or null in %v", page)
	}
	out := []map[string]any{}
	for _, e := range raw.([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

func replies(t *testing.T, owner, entryID, viewer string) []map[string]any {
	t.Helper()
	raw := get(t, publicWallOf(owner)+"/"+entryID+"/replies", viewer).
		expect(http.StatusOK).list()
	return raw
}

// userVote reads the viewer's own vote, which is omitted from JSON when unset.
func userVote(v map[string]any) string {
	if s, ok := v["userVote"]; ok && s != nil {
		return s.(string)
	}
	return ""
}

// ── entries ─────────────────────────────────────────────────────────────────

func TestGuestbookEntryLifecycle(t *testing.T) {
	reset(t)
	newProfile(t, alice, "alice_w", "everyone")
	newProfile(t, bob, "bob_w", "everyone")

	entry := postEntry(t, bob, alice, "Loved the last chapter.")
	if entry["ownerId"] != alice || entry["authorId"] != bob {
		t.Errorf("entry = %v", entry)
	}
	// The author's username is hydrated from their profile, not echoed back.
	if entry["authorUsername"] != "bob_w" {
		t.Errorf("authorUsername = %v, want bob_w", entry["authorUsername"])
	}
	if entry["commentCount"].(float64) != 0 || entry["upvoteCount"].(float64) != 0 {
		t.Errorf("new entry should have no replies or votes: %v", entry)
	}

	// The wall is publicly readable, including anonymously.
	if seen := wallEntries(t, alice, ""); len(seen) != 1 {
		t.Fatalf("anonymous read saw %d entries, want 1", len(seen))
	}

	// An author with no profile falls back to a placeholder rather than failing.
	postEntry(t, carol, alice, "No profile here.")
	found := false
	for _, e := range wallEntries(t, alice, "") {
		if e["authorId"] == carol && e["authorUsername"] == "unknown" {
			found = true
		}
	}
	if !found {
		t.Error("expected a profile-less author to read back as 'unknown'")
	}

	call(t, "DELETE", guestbookOf(alice)+"/"+entry["id"].(string), bob, nil).
		expect(http.StatusNoContent)
	if seen := wallEntries(t, alice, ""); len(seen) != 1 {
		t.Errorf("after deletion the wall has %d entries, want 1", len(seen))
	}
}

func TestGuestbookEntryDeletePermissions(t *testing.T) {
	reset(t)
	entry := postEntry(t, bob, alice, "A note.")
	id := entry["id"].(string)

	// A stranger cannot delete someone else's entry from someone else's wall.
	call(t, "DELETE", guestbookOf(alice)+"/"+id, carol, nil).expect(http.StatusForbidden)

	// The wall owner can remove anything on their own wall.
	call(t, "DELETE", guestbookOf(alice)+"/"+id, alice, nil).expect(http.StatusNoContent)

	// Deleting it again reports forbidden rather than 404 — the query cannot
	// tell "gone" from "not yours". Asserted as-is; 404 would leak less.
	call(t, "DELETE", guestbookOf(alice)+"/"+id, alice, nil).expect(http.StatusForbidden)
}

// ── the five-policy wall ────────────────────────────────────────────────────

func TestGuestbookWallPolicyMatrix(t *testing.T) {
	// Each case sets the wall owner's policy, wires the follow graph, and
	// checks who may write. alice always owns the wall; bob is the visitor.
	cases := []struct {
		policy      string
		bobFollows  bool // bob -> alice
		aliceFollow bool // alice -> bob
		allowed     bool
	}{
		{"everyone", false, false, true},
		{"nobody", false, false, false},
		{"nobody", true, true, false},

		{"followers", false, false, false},
		{"followers", true, false, true},  // bob follows alice
		{"followers", false, true, false}, // only alice follows bob

		{"following", false, false, false},
		{"following", false, true, true}, // alice follows bob
		{"following", true, false, false},

		{"mutuals", true, false, false},
		{"mutuals", false, true, false},
		{"mutuals", true, true, true},
	}

	for _, c := range cases {
		name := fmt.Sprintf("%s/bobFollows=%v/aliceFollows=%v", c.policy, c.bobFollows, c.aliceFollow)
		t.Run(name, func(t *testing.T) {
			reset(t)
			newProfile(t, alice, "alice_w", c.policy)
			if c.bobFollows {
				follow(t, bob, alice)
			}
			if c.aliceFollow {
				follow(t, alice, bob)
			}

			want := http.StatusCreated
			if !c.allowed {
				want = http.StatusForbidden
			}
			call(t, "POST", guestbookOf(alice), bob,
				map[string]any{"content": "hello"}).expect(want)

			// Replies obey the same wall policy as entries.
			seed := postEntry(t, alice, alice, "owner's own note")
			call(t, "POST", guestbookOf(alice)+"/"+seed["id"].(string)+"/replies", bob,
				map[string]any{"content": "reply"}).expect(want)
		})
	}
}

func TestGuestbookOwnerCanAlwaysPostAndDefaultsToEveryone(t *testing.T) {
	reset(t)

	// A wall with no profile row at all defaults to "everyone".
	postEntry(t, bob, alice, "No profile, still open.")

	// The owner can always write on their own wall, even at "nobody".
	newProfile(t, alice, "alice_w", "nobody")
	postEntry(t, alice, alice, "My own wall.")
	call(t, "POST", guestbookOf(alice), bob, map[string]any{"content": "blocked"}).
		expect(http.StatusForbidden)
}

// ── replies ─────────────────────────────────────────────────────────────────

// Listing replies is the endpoint that never worked: its query bound the wall
// owner as $2 and then never referenced it, so PostgreSQL could not infer the
// parameter's type and refused to prepare the statement.
func TestGuestbookRepliesListAndThread(t *testing.T) {
	reset(t)
	newProfile(t, bob, "bob_w", "everyone")
	entry := postEntry(t, bob, alice, "Top level.")
	id := entry["id"].(string)

	first := postReply(t, alice, alice, id, "", "Thanks!")
	if first["parentId"] != nil {
		t.Errorf("a top-level reply should have a null parentId, got %v", first["parentId"])
	}
	nested := postReply(t, bob, alice, id, first["id"].(string), "You're welcome.")
	if nested["parentId"] != first["id"] {
		t.Errorf("parentId = %v, want %v", nested["parentId"], first["id"])
	}

	all := replies(t, alice, id, "")
	if len(all) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(all))
	}
	for _, r := range all {
		if r["entryId"] != id {
			t.Errorf("reply carries the wrong entryId: %v", r)
		}
	}

	// The entry's reply count reflects the thread.
	for _, e := range wallEntries(t, alice, "") {
		if e["id"] == id && e["commentCount"].(float64) != 2 {
			t.Errorf("commentCount = %v, want 2", e["commentCount"])
		}
	}

	// A parent from a different entry is refused.
	other := postEntry(t, alice, alice, "Another entry.")
	call(t, "POST", guestbookOf(alice)+"/"+other["id"].(string)+"/replies", alice,
		map[string]any{"content": "x", "parentId": first["id"]}).
		expect(http.StatusNotFound)

	// Replies on an entry that is not on this wall are refused.
	call(t, "POST", guestbookOf(bob)+"/"+id+"/replies", alice,
		map[string]any{"content": "x"}).expect(http.StatusNotFound)

	// An entry with no replies returns [], never null.
	if r := replies(t, alice, other["id"].(string), ""); len(r) != 0 {
		t.Errorf("expected no replies, got %d", len(r))
	}
}

func TestGuestbookReplyEditAndDeletePermissions(t *testing.T) {
	reset(t)
	entry := postEntry(t, bob, alice, "Bob writes on Alice's wall.")
	id := entry["id"].(string)
	reply := postReply(t, carol, alice, id, "", "Carol replies.")
	replyID := reply["id"].(string)
	base := guestbookOf(alice) + "/" + id + "/replies/" + replyID

	// Only the reply's own author may edit it — not the wall owner, not the
	// entry's author.
	call(t, "PATCH", base, alice, map[string]any{"content": "edited"}).
		expect(http.StatusForbidden)
	call(t, "PATCH", base, bob, map[string]any{"content": "edited"}).
		expect(http.StatusForbidden)
	edited := call(t, "PATCH", base, carol, map[string]any{"content": "edited"}).
		expect(http.StatusOK).json()
	if edited["content"] != "edited" {
		t.Errorf("content = %v", edited["content"])
	}

	// Deletion is wider: the reply author, the wall owner, and the entry's
	// author may all remove it. A stranger may not.
	call(t, "DELETE", base, dave, nil).expect(http.StatusForbidden)
	call(t, "DELETE", base, bob, nil).expect(http.StatusNoContent)

	if r := replies(t, alice, id, ""); len(r) != 0 {
		t.Errorf("reply survived deletion: %v", r)
	}
}

// ── votes ───────────────────────────────────────────────────────────────────

func TestGuestbookVotesOnEntriesAndReplies(t *testing.T) {
	reset(t)
	entry := postEntry(t, bob, alice, "Vote on me.")
	id := entry["id"].(string)
	reply := postReply(t, bob, alice, id, "", "And me.")
	replyID := reply["id"].(string)

	votes := guestbookOf(alice) + "/" + id + "/votes"
	replyVotes := guestbookOf(alice) + "/" + id + "/replies/" + replyID + "/votes"

	call(t, "PUT", votes, carol, map[string]any{"vote": "up"}).expect(http.StatusNoContent)
	call(t, "PUT", votes, dave, map[string]any{"vote": "down"}).expect(http.StatusNoContent)

	seen := wallEntries(t, alice, carol)[0]
	if seen["upvoteCount"].(float64) != 1 || seen["downvoteCount"].(float64) != 1 {
		t.Errorf("counts = up %v / down %v", seen["upvoteCount"], seen["downvoteCount"])
	}
	if userVote(seen) != "up" {
		t.Errorf("carol's own vote = %q, want up", userVote(seen))
	}
	// Another viewer sees the same counts but their own (absent) vote.
	if v := userVote(wallEntries(t, alice, erin)[0]); v != "" {
		t.Errorf("erin has not voted but sees %q", v)
	}

	// Switching a vote replaces it rather than stacking.
	call(t, "PUT", votes, carol, map[string]any{"vote": "down"}).expect(http.StatusNoContent)
	seen = wallEntries(t, alice, carol)[0]
	if seen["upvoteCount"].(float64) != 0 || seen["downvoteCount"].(float64) != 2 {
		t.Errorf("after switching: up %v / down %v", seen["upvoteCount"], seen["downvoteCount"])
	}

	// An empty vote clears it.
	call(t, "PUT", votes, carol, map[string]any{"vote": ""}).expect(http.StatusNoContent)
	seen = wallEntries(t, alice, carol)[0]
	if seen["downvoteCount"].(float64) != 1 || userVote(seen) != "" {
		t.Errorf("vote was not cleared: %v", seen)
	}

	// Replies carry their own independent tally.
	call(t, "PUT", replyVotes, carol, map[string]any{"vote": "up"}).expect(http.StatusNoContent)
	r := replies(t, alice, id, carol)[0]
	if r["upvoteCount"].(float64) != 1 || userVote(r) != "up" {
		t.Errorf("reply vote = %v", r)
	}

	// An unknown vote value is refused.
	call(t, "PUT", votes, carol, map[string]any{"vote": "sideways"}).
		expect(http.StatusUnprocessableEntity)
	// Voting requires authentication.
	call(t, "PUT", votes, "", map[string]any{"vote": "up"}).expect(http.StatusUnauthorized)
}

// ── pagination, quota, validation ───────────────────────────────────────────

func TestGuestbookPaginationCoversEveryEntryExactlyOnce(t *testing.T) {
	reset(t)
	const total = 5
	for i := 0; i < total; i++ {
		postEntry(t, bob, alice, fmt.Sprintf("entry %d", i))
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		url := publicWallOf(alice) + "?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		page := get(t, url, "").expect(http.StatusOK).json()
		entries := page["entries"].([]any)
		if len(entries) > 2 {
			t.Fatalf("page returned %d entries, limit was 2", len(entries))
		}
		for _, raw := range entries {
			e := raw.(map[string]any)
			id := e["id"].(string)
			if seen[id] {
				t.Errorf("entry %s appeared on two pages", id)
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
		t.Errorf("pagination covered %d of %d entries", len(seen), total)
	}

	// A malformed cursor is bad input, not a server error.
	get(t, publicWallOf(alice)+"?cursor=not-base64!!", "").
		expect(http.StatusUnprocessableEntity)
	// An out-of-range limit is refused before it reaches the query.
	get(t, publicWallOf(alice)+"?limit=0", "").expect(http.StatusBadRequest)
	get(t, publicWallOf(alice)+"?limit=51", "").expect(http.StatusBadRequest)
	get(t, publicWallOf(alice)+"?limit=abc", "").expect(http.StatusBadRequest)
}

func TestGuestbookDailyQuota(t *testing.T) {
	reset(t)
	// Ten entries a day per author, enforced atomically in the same
	// transaction as the insert.
	for i := 0; i < 10; i++ {
		postEntry(t, bob, alice, fmt.Sprintf("entry %d", i))
	}
	call(t, "POST", guestbookOf(alice), bob, map[string]any{"content": "one too many"}).
		expect(http.StatusTooManyRequests)

	// The ceiling is per author, so carol is unaffected.
	postEntry(t, carol, alice, "still fine")

	// Replies carry a separate ten-a-day budget.
	entry := wallEntries(t, alice, "")[0]["id"].(string)
	for i := 0; i < 10; i++ {
		postReply(t, bob, alice, entry, "", fmt.Sprintf("reply %d", i))
	}
	call(t, "POST", guestbookOf(alice)+"/"+entry+"/replies", bob,
		map[string]any{"content": "one too many"}).expect(http.StatusTooManyRequests)

	var entries, replies int
	if err := testPool.QueryRow(context.Background(),
		`SELECT entry_count, reply_count FROM guestbook_daily_usage WHERE user_id=$1 AND day=current_date`,
		bob).Scan(&entries, &replies); err != nil {
		t.Fatal(err)
	}
	// A rejected write must not have consumed budget.
	if entries != 10 || replies != 10 {
		t.Errorf("usage = %d entries / %d replies, want 10 / 10", entries, replies)
	}
}

func TestGuestbookContentValidation(t *testing.T) {
	reset(t)

	call(t, "POST", guestbookOf(alice), bob, map[string]any{"content": "   "}).
		expect(http.StatusUnprocessableEntity)
	long := make([]byte, 10001)
	for i := range long {
		long[i] = 'x'
	}
	call(t, "POST", guestbookOf(alice), bob, map[string]any{"content": string(long)}).
		expect(http.StatusUnprocessableEntity)
	call(t, "POST", guestbookOf(alice), "", map[string]any{"content": "x"}).
		expect(http.StatusUnauthorized)

	entry := postEntry(t, bob, alice, "ok")
	call(t, "POST", guestbookOf(alice)+"/"+entry["id"].(string)+"/replies", bob,
		map[string]any{"content": ""}).expect(http.StatusUnprocessableEntity)
}

func TestGuestbookUnknownAndMalformedIDs(t *testing.T) {
	reset(t)
	absent := "11111111-1111-1111-1111-111111111111"

	// Malformed ids are 404s rather than 500s from the uuid cast.
	call(t, "DELETE", guestbookOf(alice)+"/not-a-uuid", alice, nil).expect(http.StatusNotFound)
	call(t, "PUT", guestbookOf(alice)+"/not-a-uuid/votes", alice,
		map[string]any{"vote": "up"}).expect(http.StatusNotFound)
	get(t, publicWallOf(alice)+"/not-a-uuid/replies", alice).expect(http.StatusNotFound)

	// Well-formed but absent ids are 404 on reads and votes.
	get(t, publicWallOf(alice)+"/"+absent+"/replies", alice).expect(http.StatusNotFound)
	call(t, "PUT", guestbookOf(alice)+"/"+absent+"/votes", alice,
		map[string]any{"vote": "up"}).expect(http.StatusNotFound)
	call(t, "POST", guestbookOf(alice)+"/"+absent+"/replies", alice,
		map[string]any{"content": "x"}).expect(http.StatusNotFound)

	// A wall belonging to nobody is empty rather than missing.
	if e := wallEntries(t, "user-nobody", ""); len(e) != 0 {
		t.Errorf("expected an empty wall, got %d entries", len(e))
	}
}

// ── follows ─────────────────────────────────────────────────────────────────

func TestFollowGraph(t *testing.T) {
	reset(t)

	follow(t, alice, bob)
	// Following twice is idempotent.
	follow(t, alice, bob)
	follow(t, carol, bob)

	mine := get(t, "/v1/me/follows", alice).expect(http.StatusOK).json()
	if len(mine["following"].([]any)) != 1 {
		t.Errorf("alice follows %v, want 1", mine["following"])
	}
	if len(mine["followers"].([]any)) != 0 {
		t.Errorf("alice has followers %v, want none", mine["followers"])
	}

	theirs := get(t, "/v1/me/follows", bob).expect(http.StatusOK).json()
	if len(theirs["followers"].([]any)) != 2 {
		t.Errorf("bob's followers = %v, want 2", theirs["followers"])
	}

	// Self-following is refused by both the check and the schema.
	call(t, "PUT", followPath(alice), alice, nil).expect(http.StatusUnprocessableEntity)

	call(t, "DELETE", followPath(bob), alice, nil).expect(http.StatusNoContent)
	after := get(t, "/v1/me/follows", alice).expect(http.StatusOK).json()
	if len(after["following"].([]any)) != 0 {
		t.Errorf("unfollow left %v", after["following"])
	}
	// Unfollowing again is idempotent.
	call(t, "DELETE", followPath(bob), alice, nil).expect(http.StatusNoContent)

	get(t, "/v1/me/follows", "").expect(http.StatusUnauthorized)
}
