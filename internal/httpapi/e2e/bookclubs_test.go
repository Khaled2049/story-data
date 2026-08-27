package e2e

// Book clubs: clubs, membership, reading progress, discussion prompts and
// polls.
//
// This is the largest store file in the service and had no coverage at all.
// Two things make it worth testing carefully rather than quickly: almost every
// rule here is an authorization rule enforced in Go (owner vs member vs
// stranger, applied differently per endpoint), and the read path fans out per
// club, per prompt, per response and per poll — so there is a query-count test
// at the bottom that pins the fan-out flat.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

const frank = "user-frank"

func clubPath(id string) string { return "/v1/book-clubs/" + id }

func newClub(t *testing.T, uid, name string) map[string]any {
	t.Helper()
	return call(t, "POST", "/v1/book-clubs", uid, map[string]any{
		"name": name, "description": "d", "image": "", "category": "c",
		"activity": "a", "meetUp": "",
	}).expect(http.StatusCreated).json()
}

func joinClub(t *testing.T, uid, clubID string) {
	t.Helper()
	call(t, "PUT", clubPath(clubID)+"/members/me", uid, nil).expect(http.StatusNoContent)
}

func newPrompt(t *testing.T, owner, clubID string, chapter int, question string) map[string]any {
	t.Helper()
	return call(t, "POST", clubPath(clubID)+"/prompts", owner, map[string]any{
		"chapterNumber": chapter, "question": question, "description": "",
	}).expect(http.StatusCreated).json()
}

func newPoll(t *testing.T, owner, clubID, question string) map[string]any {
	t.Helper()
	return call(t, "POST", clubPath(clubID)+"/polls", owner, map[string]any{
		"type": "book-selection", "question": question, "endDate": "",
		"options": []map[string]any{{"text": "One"}, {"text": "Two"}},
	}).expect(http.StatusCreated).json()
}

func clubMembers(t *testing.T, club map[string]any) []string {
	t.Helper()
	out := []string{}
	for _, m := range club["members"].([]any) {
		out = append(out, m.(string))
	}
	return out
}

// ── clubs ───────────────────────────────────────────────────────────────────

func TestBookClubCRUD(t *testing.T) {
	reset(t)

	club := newClub(t, alice, "The Tuesday Readers")
	id := club["id"].(string)
	if club["creatorId"] != alice || club["name"] != "The Tuesday Readers" {
		t.Errorf("club = %v", club)
	}
	// Creating a club enrols the owner as its first member.
	if m := clubMembers(t, club); len(m) != 1 || m[0] != alice {
		t.Errorf("members = %v, want [%s]", m, alice)
	}
	// Collections are [] rather than null on a brand-new club.
	if club["discussionPrompts"] == nil || club["polls"] == nil {
		t.Errorf("prompts/polls serialized as null: %v", club)
	}

	// The club document is public — no credentials needed.
	if got := get(t, clubPath(id), "").expect(http.StatusOK).json(); got["id"] != id {
		t.Errorf("anonymous read failed: %v", got)
	}

	updated := call(t, "PATCH", clubPath(id), alice, map[string]any{
		"name": "The Wednesday Readers", "description": "d2", "image": "",
		"category": "c", "activity": "a", "meetUp": "",
	}).expect(http.StatusOK).json()
	if updated["name"] != "The Wednesday Readers" {
		t.Errorf("name = %v", updated["name"])
	}

	// Only the owner may edit or delete.
	call(t, "PATCH", clubPath(id), bob, map[string]any{
		"name": "Stolen", "description": "", "image": "", "category": "",
		"activity": "", "meetUp": "",
	}).expect(http.StatusForbidden)
	call(t, "DELETE", clubPath(id), bob, nil).expect(http.StatusForbidden)

	call(t, "DELETE", clubPath(id), alice, nil).expect(http.StatusNoContent)
	get(t, clubPath(id), alice).expect(http.StatusNotFound)

	// Members cascade with the club.
	var members int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM book_club_members WHERE club_id=$1`, id).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 0 {
		t.Errorf("expected membership to cascade, %d rows remain", members)
	}
}

func TestBookClubValidationAndAuth(t *testing.T) {
	reset(t)

	call(t, "POST", "/v1/book-clubs", alice, map[string]any{
		"name": "   ", "description": "", "image": "", "category": "",
		"activity": "", "meetUp": "",
	}).expect(http.StatusUnprocessableEntity)

	call(t, "POST", "/v1/book-clubs", "", map[string]any{
		"name": "x", "description": "", "image": "", "category": "",
		"activity": "", "meetUp": "",
	}).expect(http.StatusUnauthorized)

	// Listing is public.
	get(t, "/v1/book-clubs", "").expect(http.StatusOK).list()
}

// Writes decode with DisallowUnknownFields, so a client that echoes back a
// whole club object gets a 400 for the request rather than having the
// server-assigned fields ignored. Pinned because the shape of the rejection
// is what tells a client it must map to the input contract.
func TestBookClubRejectsServerAssignedFields(t *testing.T) {
	reset(t)

	body := func(extra string, value any) map[string]any {
		return map[string]any{
			"name": "Echoed", "description": "d", "image": "", "category": "c",
			"activity": "a", "meetUp": "", extra: value,
		}
	}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"id", "client-generated-uuid"},
		{"creatorId", alice},
		{"members", []string{alice}},
	} {
		call(t, "POST", "/v1/book-clubs", alice, body(field.name, field.value)).
			expect(http.StatusBadRequest)
	}

	// The mapped body — exactly the declared input fields — is accepted, and
	// the server fills in the identity the client was trying to send.
	club := call(t, "POST", "/v1/book-clubs", alice, map[string]any{
		"name": "Mapped", "description": "d", "image": "", "category": "c",
		"activity": "a", "meetUp": "",
	}).expect(http.StatusCreated).json()
	if club["id"] == "" || club["id"] == nil {
		t.Error("server should assign an id")
	}
}

// The nested club writes decode with DisallowUnknownFields, so echoing a whole
// domain object back — the id, the author, the timestamp the server assigns —
// fails the request outright rather than having the extras ignored. Pinned per
// endpoint because each one was broken this way independently.
func TestClubWritesRejectServerAssignedFields(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Contracts")["id"].(string)
	promptID := newPrompt(t, alice, id, 1, "Q")["id"].(string)

	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"prompt createdAt", clubPath(id) + "/prompts", map[string]any{
			"chapterNumber": 1, "question": "Q", "description": "",
			"createdAt": "2026-08-26T00:00:00Z",
		}},
		{"prompt creatorId", clubPath(id) + "/prompts", map[string]any{
			"chapterNumber": 1, "question": "Q", "description": "", "creatorId": alice,
		}},
		{"prompt responses", clubPath(id) + "/prompts", map[string]any{
			"chapterNumber": 1, "question": "Q", "description": "",
			"responses": []any{},
		}},
		{"response userId", clubPath(id) + "/prompts/" + promptID + "/responses",
			map[string]any{"content": "c", "userId": alice}},
		{"response createdAt", clubPath(id) + "/prompts/" + promptID + "/responses",
			map[string]any{"content": "c", "createdAt": "2026-08-26T00:00:00Z"}},
		{"poll votes", clubPath(id) + "/polls", map[string]any{
			"type": "book-selection", "question": "Q", "endDate": "",
			"options": []map[string]any{{"text": "One"}, {"text": "Two"}},
			"votes":   map[string]any{},
		}},
		{"poll isActive", clubPath(id) + "/polls", map[string]any{
			"type": "book-selection", "question": "Q", "endDate": "",
			"options":  []map[string]any{{"text": "One"}, {"text": "Two"}},
			"isActive": true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call(t, "POST", tc.path, alice, tc.body).expect(http.StatusBadRequest)
		})
	}

	// The mapped bodies — exactly the declared input fields — are accepted, and
	// the server fills in the identity and timestamps the client tried to send.
	got := call(t, "POST", clubPath(id)+"/prompts/"+promptID+"/responses", alice,
		map[string]any{"content": "mapped"}).expect(http.StatusCreated).json()
	if got["userId"] != alice || got["createdAt"] == nil {
		t.Errorf("server should assign userId and createdAt, got %v", got)
	}
}

// novelsyncBook is the snapshot the client's storyToBook builds when an owner
// picks a NovelSync story as the club's book.
func novelsyncBook(storyID, title string) map[string]any {
	return map[string]any{
		"id": storyID, "source": "novelsync", "storyId": storyID,
		"volumeInfo": map[string]any{"title": title, "authors": []string{"a"}},
	}
}

func setClubBook(t *testing.T, uid, clubID string, book map[string]any, want int) {
	t.Helper()
	call(t, "PATCH", clubPath(clubID)+"/settings", uid,
		map[string]any{"bookOfTheMonth": book}).expect(want)
}

// A club book is a snapshot, so it cannot vouch for itself. The picker only
// offers published stories, but unpublishing afterwards used to leave the
// club advertising a draft's title, author and cover to every member.
func TestClubBookIsHiddenOnceItsStoryIsUnpublished(t *testing.T) {
	reset(t)
	story := newPublishedStory(t, bob, "Piranesi")
	storyID := story["id"].(string)
	id := newClub(t, alice, "Readers")["id"].(string)

	setClubBook(t, alice, id, novelsyncBook(storyID, "Piranesi"), http.StatusOK)
	if got := get(t, clubPath(id), alice).expect(http.StatusOK).json(); got["bookOfTheMonth"] == nil {
		t.Fatal("a published story should be visible as the club book")
	}

	// The author takes it private again.
	call(t, "PATCH", "/v1/stories/"+storyID, bob, map[string]any{
		"title": "Piranesi", "description": "d", "authorName": "a",
		"tags": []string{"x"}, "published": false,
	}, ifMatch(int64(story["revision"].(float64)))).expect(http.StatusOK)

	got := get(t, clubPath(id), alice).expect(http.StatusOK).json()
	if got["bookOfTheMonth"] != nil {
		t.Errorf("club still shows an unpublished story: %v", got["bookOfTheMonth"])
	}
	// The listing is the other read path and must agree.
	for _, c := range get(t, "/v1/book-clubs", alice).expect(http.StatusOK).list() {
		if c["id"] == id && c["bookOfTheMonth"] != nil {
			t.Errorf("listing still shows an unpublished story: %v", c["bookOfTheMonth"])
		}
	}
}

// The client picker only lists published stories, so this closes the path
// around it: PATCHing the settings endpoint directly.
func TestClubBookRejectsAnUnpublishedStory(t *testing.T) {
	reset(t)
	// Alice's own draft — the case where she has every right to read it, but
	// the club's members do not.
	draft := newStory(t, alice, "Alice's draft")
	id := newClub(t, alice, "Readers")["id"].(string)

	setClubBook(t, alice, id, novelsyncBook(draft["id"].(string), "Alice's draft"),
		http.StatusUnprocessableEntity)
	setClubBook(t, alice, id, novelsyncBook(uuid.NewString(), "Ghost"),
		http.StatusUnprocessableEntity)
	// Malformed ids are a miss, not a query error.
	setClubBook(t, alice, id, novelsyncBook("not-a-uuid", "Ghost"),
		http.StatusUnprocessableEntity)

	if got := get(t, clubPath(id), alice).expect(http.StatusOK).json(); got["bookOfTheMonth"] != nil {
		t.Errorf("a refused book was stored anyway: %v", got["bookOfTheMonth"])
	}
}

// Google books have no story to verify, and a legacy book with no source is
// treated as Google. Neither may be swept up by the published check.
func TestClubBookLeavesNonStoryBooksAlone(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Readers")["id"].(string)

	google := map[string]any{
		"id": "gbooks-1", "source": "google",
		"volumeInfo": map[string]any{"title": "Dune"},
	}
	setClubBook(t, alice, id, google, http.StatusOK)
	if got := get(t, clubPath(id), alice).expect(http.StatusOK).json(); got["bookOfTheMonth"] == nil {
		t.Fatal("a Google book was hidden by the published check")
	}

	legacy := map[string]any{"id": "legacy-1", "volumeInfo": map[string]any{"title": "Old"}}
	setClubBook(t, alice, id, legacy, http.StatusOK)
	book, _ := get(t, clubPath(id), alice).expect(http.StatusOK).json()["bookOfTheMonth"].(map[string]any)
	if book == nil || book["id"] != "legacy-1" {
		t.Errorf("a sourceless legacy book was hidden: %v", book)
	}
}

func TestBookClubSettings(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Settings")["id"].(string)
	settings := clubPath(id) + "/settings"

	got := call(t, "PATCH", settings, alice, map[string]any{
		"meetUp":         "Thursdays, 7pm",
		"bookOfTheMonth": map[string]any{"title": "Piranesi"},
		"readingSchedule": map[string]any{
			"chapters": []map[string]any{{"chapter": 1, "due": "2026-09-01"}},
		},
	}).expect(http.StatusOK).json()
	if got["meetUp"] != "Thursdays, 7pm" {
		t.Errorf("meetUp = %v", got["meetUp"])
	}
	if got["bookOfTheMonth"].(map[string]any)["title"] != "Piranesi" {
		t.Errorf("bookOfTheMonth = %v", got["bookOfTheMonth"])
	}

	// Omitted fields are left alone rather than cleared.
	partial := call(t, "PATCH", settings, alice, map[string]any{
		"meetUp": "Fridays",
	}).expect(http.StatusOK).json()
	if partial["bookOfTheMonth"] == nil {
		t.Error("a partial settings patch cleared bookOfTheMonth")
	}

	// Pinned as-is: because the update COALESCEs a JSON null back to the
	// current value, there is no way to clear these fields once set. Sending
	// an explicit null is a no-op, not a delete.
	cleared := call(t, "PATCH", settings, alice, map[string]any{
		"bookOfTheMonth": nil,
	}).expect(http.StatusOK).json()
	if cleared["bookOfTheMonth"] == nil {
		t.Error("bookOfTheMonth became clearable — the API contract changed")
	}

	call(t, "PATCH", settings, bob, map[string]any{"meetUp": "Mine now"}).
		expect(http.StatusForbidden)
}

// ── membership ──────────────────────────────────────────────────────────────

func TestBookClubJoinAndLeave(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Joinable")["id"].(string)

	joinClub(t, bob, id)
	// Joining twice is idempotent.
	joinClub(t, bob, id)

	club := get(t, clubPath(id), "").expect(http.StatusOK).json()
	if m := clubMembers(t, club); len(m) != 2 {
		t.Errorf("members = %v, want 2", m)
	}

	call(t, "DELETE", clubPath(id)+"/members/me", bob, nil).expect(http.StatusNoContent)
	if m := clubMembers(t, get(t, clubPath(id), "").expect(http.StatusOK).json()); len(m) != 1 {
		t.Errorf("after leaving, members = %v, want 1", m)
	}

	// Leaving is idempotent: a DELETE for a membership that is already gone
	// succeeds rather than reporting forbidden.
	call(t, "DELETE", clubPath(id)+"/members/me", bob, nil).expect(http.StatusNoContent)
	// A user who never joined is likewise not an error.
	call(t, "DELETE", clubPath(id)+"/members/me", carol, nil).expect(http.StatusNoContent)

	// The owner cannot leave their own club — they must delete it.
	call(t, "DELETE", clubPath(id)+"/members/me", alice, nil).expect(http.StatusForbidden)
}

func TestBookClubCapacity(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Small")["id"].(string)

	// Capacity counts the owner, so a 10-seat club takes 9 joiners.
	for i := 0; i < 9; i++ {
		joinClub(t, fmt.Sprintf("user-filler-%d", i), id)
	}
	call(t, "PUT", clubPath(id)+"/members/me", bob, nil).
		expect(http.StatusUnprocessableEntity)

	// An existing member re-joining a full club is still fine.
	call(t, "PUT", clubPath(id)+"/members/me", "user-filler-0", nil).
		expect(http.StatusNoContent)

	call(t, "PUT", clubPath("11111111-1111-1111-1111-111111111111")+"/members/me",
		bob, nil).expect(http.StatusNotFound)
}

// ── reading progress ────────────────────────────────────────────────────────

func TestBookClubProgress(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Progress")["id"].(string)
	joinClub(t, bob, id)
	progress := clubPath(id) + "/progress"

	saved := call(t, "PUT", progress+"/me", bob,
		map[string]any{"currentChapter": 4, "notes": "gripping"}).
		expect(http.StatusOK).json()
	if saved["currentChapter"].(float64) != 4 || saved["userId"] != bob {
		t.Errorf("saved = %v", saved)
	}

	// Saving again replaces rather than appends.
	call(t, "PUT", progress+"/me", bob,
		map[string]any{"currentChapter": 7, "notes": nil}).expect(http.StatusOK)

	all := get(t, progress, bob).expect(http.StatusOK).list()
	if len(all) != 1 || all[0]["currentChapter"].(float64) != 7 {
		t.Errorf("progress = %v", all)
	}

	// Progress is member-only, on both read and write.
	get(t, progress, carol).expect(http.StatusForbidden)
	call(t, "PUT", progress+"/me", carol, map[string]any{"currentChapter": 1}).
		expect(http.StatusForbidden)
	get(t, progress, "").expect(http.StatusUnauthorized)

	// A negative chapter is bad input, not a permission problem.
	call(t, "PUT", progress+"/me", bob, map[string]any{"currentChapter": -1}).
		expect(http.StatusUnprocessableEntity)

	// A member who leaves stops appearing in the roster's progress.
	call(t, "DELETE", clubPath(id)+"/members/me", bob, nil).expect(http.StatusNoContent)
	if left := get(t, progress, alice).expect(http.StatusOK).list(); len(left) != 0 {
		t.Errorf("progress for a departed member is still listed: %v", left)
	}
}

// ── discussion prompts ──────────────────────────────────────────────────────

func TestBookClubPrompts(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Discussions")["id"].(string)
	joinClub(t, bob, id)

	prompt := newPrompt(t, alice, id, 3, "What did you make of the ending?")
	if prompt["chapterNumber"].(float64) != 3 || prompt["creatorId"] != alice {
		t.Errorf("prompt = %v", prompt)
	}
	if prompt["responses"] == nil {
		t.Error("responses serialized as null, want []")
	}

	// Prompts are owner-only; a plain member cannot create one.
	call(t, "POST", clubPath(id)+"/prompts", bob, map[string]any{
		"chapterNumber": 1, "question": "Mine", "description": "",
	}).expect(http.StatusForbidden)

	// Validation: chapter must be >= 1, question non-empty.
	call(t, "POST", clubPath(id)+"/prompts", alice, map[string]any{
		"chapterNumber": 0, "question": "q", "description": "",
	}).expect(http.StatusUnprocessableEntity)
	call(t, "POST", clubPath(id)+"/prompts", alice, map[string]any{
		"chapterNumber": 1, "question": "   ", "description": "",
	}).expect(http.StatusUnprocessableEntity)

	// Responses are member-only.
	responses := clubPath(id) + "/prompts/" + prompt["id"].(string) + "/responses"
	answer := call(t, "POST", responses, bob, map[string]any{"content": "Loved it."}).
		expect(http.StatusCreated).json()
	if answer["userId"] != bob || answer["content"] != "Loved it." {
		t.Errorf("response = %v", answer)
	}
	call(t, "POST", responses, carol, map[string]any{"content": "Sneaking in."}).
		expect(http.StatusForbidden)
	call(t, "POST", responses, bob, map[string]any{"content": ""}).
		expect(http.StatusUnprocessableEntity)

	// The prompt and its responses read back nested in the club document.
	club := get(t, clubPath(id), "").expect(http.StatusOK).json()
	prompts := club["discussionPrompts"].([]any)
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}
	if got := prompts[0].(map[string]any)["responses"].([]any); len(got) != 1 {
		t.Errorf("expected 1 response nested in the club, got %d", len(got))
	}

	// A prompt belonging to another club is not reachable through this one.
	other := newClub(t, alice, "Elsewhere")["id"].(string)
	call(t, "POST", clubPath(other)+"/prompts/"+prompt["id"].(string)+"/responses",
		alice, map[string]any{"content": "x"}).expect(http.StatusNotFound)
}

// ── polls ───────────────────────────────────────────────────────────────────

func TestBookClubPolls(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Polls")["id"].(string)
	joinClub(t, bob, id)
	joinClub(t, carol, id)

	poll := newPoll(t, alice, id, "What next?")
	pollID := poll["id"].(string)
	if len(poll["options"].([]any)) != 2 || poll["isActive"] != true {
		t.Errorf("poll = %v", poll)
	}
	if poll["type"] != "book-selection" {
		t.Errorf("type = %v", poll["type"])
	}

	// Creating a poll is owner-only, and that is a permission failure rather
	// than a malformed request.
	call(t, "POST", clubPath(id)+"/polls", bob, map[string]any{
		"type": "", "question": "Mine", "endDate": "",
		"options": []map[string]any{{"text": "a"}, {"text": "b"}},
	}).expect(http.StatusForbidden)

	// Option count is bounded at both ends.
	call(t, "POST", clubPath(id)+"/polls", alice, map[string]any{
		"type": "", "question": "Too few", "endDate": "",
		"options": []map[string]any{{"text": "only"}},
	}).expect(http.StatusUnprocessableEntity)

	vote := clubPath(id) + "/polls/" + pollID + "/vote"
	call(t, "PUT", vote, bob, map[string]any{"optionIndex": 0}).expect(http.StatusNoContent)
	call(t, "PUT", vote, carol, map[string]any{"optionIndex": 1}).expect(http.StatusNoContent)
	// Re-voting replaces the previous choice.
	call(t, "PUT", vote, bob, map[string]any{"optionIndex": 1}).expect(http.StatusNoContent)

	club := get(t, clubPath(id), "").expect(http.StatusOK).json()
	votes := club["polls"].([]any)[0].(map[string]any)["votes"].(map[string]any)
	if len(votes) != 2 || votes[bob].(float64) != 1 || votes[carol].(float64) != 1 {
		t.Errorf("votes = %v", votes)
	}

	// Voting is member-only, and a negative index is bad input, not a
	// permission failure.
	call(t, "PUT", vote, dave, map[string]any{"optionIndex": 0}).expect(http.StatusForbidden)
	call(t, "PUT", vote, bob, map[string]any{"optionIndex": -1}).
		expect(http.StatusUnprocessableEntity)
	// An index past the end of the options is also bad input.
	call(t, "PUT", vote, bob, map[string]any{"optionIndex": 99}).
		expect(http.StatusUnprocessableEntity)
	// A poll that does not exist is a 404, distinct from a bad index.
	call(t, "PUT", clubPath(id)+"/polls/11111111-1111-1111-1111-111111111111/vote",
		bob, map[string]any{"optionIndex": 0}).expect(http.StatusNotFound)

	// Closing is owner-only and idempotent.
	closePath := clubPath(id) + "/polls/" + pollID + "/close"
	call(t, "PUT", closePath, bob, nil).expect(http.StatusForbidden)
	call(t, "PUT", closePath, alice, nil).expect(http.StatusNoContent)
	call(t, "PUT", closePath, alice, nil).expect(http.StatusNoContent)

	// A closed poll refuses further votes, and says so distinctly from a
	// missing poll or a bad index.
	call(t, "PUT", vote, bob, map[string]any{"optionIndex": 0}).expect(http.StatusConflict)

	after := get(t, clubPath(id), "").expect(http.StatusOK).json()
	if after["polls"].([]any)[0].(map[string]any)["isActive"] != false {
		t.Error("poll should read back as closed")
	}
}

// ── quotas ──────────────────────────────────────────────────────────────────

func TestBookClubPromptQuotaIsPerUserAcrossClubs(t *testing.T) {
	reset(t)
	first := newClub(t, alice, "Busy")["id"].(string)
	second := newClub(t, alice, "Also busy")["id"].(string)

	// Ten prompts a day — the budget is per user, not per club, so it is
	// already spent by the time the second club is touched.
	for i := 0; i < 10; i++ {
		newPrompt(t, alice, first, i+1, fmt.Sprintf("Question %d", i))
	}
	call(t, "POST", clubPath(first)+"/prompts", alice, map[string]any{
		"chapterNumber": 99, "question": "One too many", "description": "",
	}).expect(http.StatusTooManyRequests)
	call(t, "POST", clubPath(second)+"/prompts", alice, map[string]any{
		"chapterNumber": 1, "question": "Different club, same budget", "description": "",
	}).expect(http.StatusTooManyRequests)

	// The ceiling is per user, so bob's own club is unaffected.
	bobs := newClub(t, bob, "Bob's")["id"].(string)
	newPrompt(t, bob, bobs, 1, "Still fine")
}

func TestBookClubPollAndResponseQuotas(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Quota club")["id"].(string)
	joinClub(t, bob, id)

	// Five polls a day per user.
	for i := 0; i < 5; i++ {
		newPoll(t, alice, id, fmt.Sprintf("Poll %d", i))
	}
	call(t, "POST", clubPath(id)+"/polls", alice, map[string]any{
		"type": "", "question": "One too many", "endDate": "",
		"options": []map[string]any{{"text": "a"}, {"text": "b"}},
	}).expect(http.StatusTooManyRequests)

	// Twenty prompt responses a day per user.
	prompt := newPrompt(t, alice, id, 1, "Discuss")["id"].(string)
	responses := clubPath(id) + "/prompts/" + prompt + "/responses"
	for i := 0; i < 20; i++ {
		call(t, "POST", responses, bob, map[string]any{"content": fmt.Sprintf("r%d", i)}).
			expect(http.StatusCreated)
	}
	call(t, "POST", responses, bob, map[string]any{"content": "one too many"}).
		expect(http.StatusTooManyRequests)

	// A rejected write must not consume more budget than it used.
	var used int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count FROM book_club_usage WHERE user_id=$1 AND action='prompt-response'`,
		bob).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 20 {
		t.Errorf("prompt-response usage = %d, want 20", used)
	}
}

func TestBookClubRejectedWriteDoesNotSpendQuota(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Refunds")["id"].(string)
	other := newClub(t, alice, "Elsewhere")["id"].(string)
	joinClub(t, bob, id)
	joinClub(t, bob, other)
	prompt := newPrompt(t, alice, id, 1, "Discuss")["id"].(string)

	// Aim a response at a prompt that belongs to a different club. The write is
	// refused, and because the quota is spent in the same transaction it must
	// roll back with it.
	call(t, "POST", clubPath(other)+"/prompts/"+prompt+"/responses", bob,
		map[string]any{"content": "wrong club"}).expect(http.StatusNotFound)

	var used int
	e := testPool.QueryRow(context.Background(),
		`SELECT COALESCE((SELECT count FROM book_club_usage WHERE user_id=$1 AND action='prompt-response'), 0)`,
		bob).Scan(&used)
	if e != nil {
		t.Fatal(e)
	}
	if used != 0 {
		t.Errorf("a refused response spent %d of the daily budget, want 0", used)
	}

	// The budget is genuinely still there.
	call(t, "POST", clubPath(id)+"/prompts/"+prompt+"/responses", bob,
		map[string]any{"content": "right club"}).expect(http.StatusCreated)
}

// ── ids and routing ─────────────────────────────────────────────────────────

func TestBookClubUnknownAndMalformedIDs(t *testing.T) {
	reset(t)
	absent := "11111111-1111-1111-1111-111111111111"

	get(t, clubPath("not-a-uuid"), alice).expect(http.StatusNotFound)
	get(t, clubPath(absent), alice).expect(http.StatusNotFound)

	id := newClub(t, alice, "Real")["id"].(string)
	call(t, "POST", clubPath(id)+"/prompts/not-a-uuid/responses", alice,
		map[string]any{"content": "x"}).expect(http.StatusNotFound)
	call(t, "PUT", clubPath(id)+"/polls/not-a-uuid/vote", alice,
		map[string]any{"optionIndex": 0}).expect(http.StatusNotFound)
	call(t, "PUT", clubPath(id)+"/polls/not-a-uuid/close", alice, nil).
		expect(http.StatusNotFound)

	// Pinned as-is: the handler resolves credentials before matching the
	// sub-path, so an unauthenticated request to a route that does not exist
	// answers 401 rather than 404.
	get(t, clubPath(id)+"/bogus", "").expect(http.StatusUnauthorized)
	get(t, clubPath(id)+"/bogus", alice).expect(http.StatusNotFound)
}

// ── fan-out ─────────────────────────────────────────────────────────────────

// The club read hydrates members, prompts, responses, polls, options and votes.
// Done naively that is a query per row at four levels; this test pins the cost
// flat, because the endpoint is public, unauthenticated and unpaginated, so an
// N+1 here is reachable by anyone.
func TestBookClubListDoesNotFanOut(t *testing.T) {
	reset(t)

	// Each owner gets their own club so the per-user daily quotas are not the
	// thing under test.
	build := func(owner string, n int) {
		t.Helper()
		id := newClub(t, owner, "Club of "+owner)["id"].(string)
		for i := 0; i < n; i++ {
			p := newPrompt(t, owner, id, i+1, fmt.Sprintf("Q%d", i))["id"].(string)
			for j := 0; j < 2; j++ {
				call(t, "POST", clubPath(id)+"/prompts/"+p+"/responses", owner,
					map[string]any{"content": fmt.Sprintf("r%d", j)}).
					expect(http.StatusCreated)
			}
		}
		for i := 0; i < 2; i++ {
			newPoll(t, owner, id, fmt.Sprintf("Poll %d", i))
		}
	}

	for _, owner := range []string{alice, bob, carol} {
		build(owner, 3)
	}
	resetQueryCount()
	get(t, "/v1/book-clubs", "").expect(http.StatusOK).list()
	small := queryCount()

	for _, owner := range []string{dave, erin, frank} {
		build(owner, 3)
	}
	resetQueryCount()
	listed := get(t, "/v1/book-clubs", "").expect(http.StatusOK).list()
	large := queryCount()

	if len(listed) != 6 {
		t.Fatalf("expected 6 clubs, got %d", len(listed))
	}
	if large != small {
		t.Errorf("query count grew with the result set: %d queries for 3 clubs, "+
			"%d for 6 — the read is fanning out per row", small, large)
	}
	// A generous ceiling: the loader should need a fixed handful of statements,
	// not one per entity. Asserted as a bound so unrelated changes stay green.
	if large > 15 {
		t.Errorf("listing 6 clubs took %d queries, want a fixed small number", large)
	}
}

func TestBookClubGetDoesNotFanOut(t *testing.T) {
	reset(t)
	id := newClub(t, alice, "Deep")["id"].(string)

	for i := 0; i < 3; i++ {
		p := newPrompt(t, alice, id, i+1, fmt.Sprintf("Q%d", i))["id"].(string)
		for j := 0; j < 3; j++ {
			call(t, "POST", clubPath(id)+"/prompts/"+p+"/responses", alice,
				map[string]any{"content": fmt.Sprintf("r%d", j)}).
				expect(http.StatusCreated)
		}
	}
	newPoll(t, alice, id, "Which next?")

	resetQueryCount()
	get(t, clubPath(id), "").expect(http.StatusOK)
	if n := queryCount(); n > 15 {
		t.Errorf("reading one club took %d queries; the hydration is fanning out "+
			"per prompt, response and poll", n)
	}
}

// Club creation was the one book-club write with no budget: prompts,
// responses and polls were already metered, but a script could create clubs
// without limit.
func TestBookClubCreationIsRateLimited(t *testing.T) {
	reset(t)
	for i := 0; i < 5; i++ {
		newClub(t, alice, fmt.Sprintf("Club %d", i))
	}
	call(t, "POST", "/v1/book-clubs", alice, map[string]any{
		"name": "One too many", "description": "d", "image": "", "category": "c",
		"activity": "a", "meetUp": "",
	}).expect(http.StatusTooManyRequests)

	// One user's budget, not the platform's.
	newClub(t, bob, "Unaffected")

	// The refused club was not created.
	if n := len(get(t, "/v1/book-clubs", "").expect(http.StatusOK).list()); n != 6 {
		t.Errorf("listing holds %d clubs, want 6", n)
	}
}
