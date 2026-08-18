package e2e

// Worldbuilding: characters, places, plot lines and plot events.
//
// Every entity here is owner-scoped, revision-guarded, and — except plot
// lines, which carry no indexable content — has to enqueue an indexing_outbox
// event on write. The cross-entity references (character relationships, event
// characters, event dependencies) are the interesting part: they are validated
// in Go rather than by foreign keys, because a reference has to stay inside
// the same story.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func worldPath(storyID, kind string) string {
	return "/v1/stories/" + storyID + "/" + kind
}

// newCharacter creates a character and returns it.
func newCharacter(t *testing.T, uid, storyID, name string) map[string]any {
	t.Helper()
	return call(t, "POST", worldPath(storyID, "characters"), uid, map[string]any{
		"name": name, "soul": "s", "personality": "p",
	}).expect(http.StatusCreated).json()
}

func newPlotLine(t *testing.T, uid, storyID, name string) map[string]any {
	t.Helper()
	return call(t, "POST", worldPath(storyID, "plots"), uid, map[string]any{
		"name": name, "description": "d",
	}).expect(http.StatusCreated).json()
}

func newEvent(t *testing.T, uid, storyID, lineID, name string) map[string]any {
	t.Helper()
	return call(t, "POST", worldPath(storyID, "plots")+"/"+lineID+"/events", uid,
		map[string]any{"name": name, "content": "c"}).expect(http.StatusCreated).json()
}

// outboxCount counts pending index events for one aggregate type in a story.
func outboxCount(t *testing.T, storyID, aggregate string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM indexing_outbox WHERE story_id=$1 AND aggregate_type=$2`,
		storyID, aggregate).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

func rev(t *testing.T, v map[string]any) int64 {
	t.Helper()
	r, ok := v["revision"]
	if !ok {
		t.Fatalf("no revision in %v", v)
	}
	return int64(r.(float64))
}

// ── characters ──────────────────────────────────────────────────────────────

func TestCharacterCRUD(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Worlds")
	id := story["id"].(string)
	base := worldPath(id, "characters")

	created := call(t, "POST", base, alice, map[string]any{
		"name": "Isolde Vane", "age": 34, "soul": "stubborn", "notes": "n",
	}).expect(http.StatusCreated).json()
	if created["name"] != "Isolde Vane" {
		t.Errorf("name = %v", created["name"])
	}
	if created["age"].(float64) != 34 {
		t.Errorf("age = %v", created["age"])
	}
	// Relationships must be [] rather than null even when there are none.
	if created["relationships"] == nil {
		t.Error("relationships serialized as null, want []")
	}
	charID := created["id"].(string)

	listed := get(t, base, alice).expect(http.StatusOK).list()
	if len(listed) != 1 {
		t.Fatalf("expected 1 character, got %d", len(listed))
	}

	updated := call(t, "PATCH", base+"/"+charID, alice, map[string]any{
		"name": "Isolde Vane-Ashcroft", "soul": "less stubborn",
	}, ifMatch(rev(t, created))).expect(http.StatusOK).json()
	if updated["name"] != "Isolde Vane-Ashcroft" {
		t.Errorf("name = %v", updated["name"])
	}
	if rev(t, updated) <= rev(t, created) {
		t.Errorf("revision did not advance: %v", updated["revision"])
	}
	// Age was omitted from the patch, so it is cleared — these are full
	// replacements, not merges.
	if updated["age"] != nil {
		t.Errorf("expected the omitted age to be cleared, got %v", updated["age"])
	}

	// A stale revision conflicts rather than overwriting.
	call(t, "PATCH", base+"/"+charID, alice, map[string]any{"name": "Nope"},
		ifMatch(rev(t, created))).expect(http.StatusConflict)
	// A missing If-Match is a malformed precondition.
	call(t, "PATCH", base+"/"+charID, alice, map[string]any{"name": "Nope"}).
		expect(http.StatusPreconditionRequired)

	call(t, "DELETE", base+"/"+charID, alice, nil, ifMatch(rev(t, updated))).
		expect(http.StatusNoContent)
	if remaining := get(t, base, alice).expect(http.StatusOK).list(); len(remaining) != 0 {
		t.Errorf("character survived deletion: %v", remaining)
	}
}

func TestCharacterRequiresNameAndOwnership(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Guarded")
	id := story["id"].(string)
	base := worldPath(id, "characters")

	call(t, "POST", base, alice, map[string]any{"name": "  "}).expect(http.StatusBadRequest)
	call(t, "POST", base, "", map[string]any{"name": "x"}).expect(http.StatusUnauthorized)

	// A non-owner cannot read or write another author's cast.
	get(t, base, bob).expect(http.StatusForbidden)
	call(t, "POST", base, bob, map[string]any{"name": "Intruder"}).expect(http.StatusForbidden)

	created := newCharacter(t, alice, id, "Real")
	call(t, "PATCH", base+"/"+created["id"].(string), bob,
		map[string]any{"name": "Stolen"}, ifMatch(rev(t, created))).
		expect(http.StatusForbidden)
	call(t, "DELETE", base+"/"+created["id"].(string), bob, nil, ifMatch(rev(t, created))).
		expect(http.StatusForbidden)
}

func TestCharacterRelationshipsAreScopedToTheStory(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Cast")
	id := story["id"].(string)
	base := worldPath(id, "characters")

	a := newCharacter(t, alice, id, "Aldous")
	b := newCharacter(t, alice, id, "Beatrix")

	linked := call(t, "PATCH", base+"/"+a["id"].(string), alice, map[string]any{
		"name": "Aldous",
		"relationships": []map[string]any{
			{"characterId": b["id"], "type": "sibling", "description": "estranged"},
		},
	}, ifMatch(rev(t, a))).expect(http.StatusOK).json()

	rels := linked["relationships"].([]any)
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	// The related character's name is hydrated from the row, not echoed back
	// from the request.
	if r := rels[0].(map[string]any); r["name"] != "Beatrix" || r["type"] != "sibling" {
		t.Errorf("relationship = %v", r)
	}

	current := rev(t, linked)

	// A character cannot be related to itself.
	call(t, "PATCH", base+"/"+a["id"].(string), alice, map[string]any{
		"name":          "Aldous",
		"relationships": []map[string]any{{"characterId": a["id"], "type": "self"}},
	}, ifMatch(current)).expect(http.StatusUnprocessableEntity)

	// Nor to a character in someone else's story — these are validated in Go
	// rather than by a foreign key, because the FK alone would allow it.
	other := newStory(t, bob, "Bob's story")
	outsider := newCharacter(t, bob, other["id"].(string), "Outsider")
	call(t, "PATCH", base+"/"+a["id"].(string), alice, map[string]any{
		"name":          "Aldous",
		"relationships": []map[string]any{{"characterId": outsider["id"], "type": "rival"}},
	}, ifMatch(current)).expect(http.StatusUnprocessableEntity)

	// Nor listed twice.
	call(t, "PATCH", base+"/"+a["id"].(string), alice, map[string]any{
		"name": "Aldous",
		"relationships": []map[string]any{
			{"characterId": b["id"], "type": "sibling"},
			{"characterId": b["id"], "type": "rival"},
		},
	}, ifMatch(current)).expect(http.StatusUnprocessableEntity)

	// None of the rejected edits partially applied.
	after := get(t, base, alice).expect(http.StatusOK).list()
	for _, c := range after {
		if c["id"] == a["id"] && len(c["relationships"].([]any)) != 1 {
			t.Errorf("relationships were mutated by a rejected edit: %v", c["relationships"])
		}
	}
}

func TestCharacterWriteEnqueuesIndexingOutbox(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Indexed cast")
	id := story["id"].(string)

	c := newCharacter(t, alice, id, "Indexed")
	if outboxCount(t, id, "character") != 1 {
		t.Fatalf("create did not enqueue an index event")
	}
	call(t, "PATCH", worldPath(id, "characters")+"/"+c["id"].(string), alice,
		map[string]any{"name": "Reindexed"}, ifMatch(rev(t, c))).expect(http.StatusOK)
	if n := outboxCount(t, id, "character"); n != 2 {
		t.Errorf("update did not enqueue an index event, outbox has %d", n)
	}
}

// ── places ──────────────────────────────────────────────────────────────────

func TestPlaceCRUD(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Places")
	id := story["id"].(string)
	base := worldPath(id, "places")

	created := call(t, "POST", base, alice, map[string]any{
		"name": "The Drowned Library", "description": "d", "atmosphere": "damp",
	}).expect(http.StatusCreated).json()
	placeID := created["id"].(string)

	if get(t, base, alice).expect(http.StatusOK).list()[0]["name"] != "The Drowned Library" {
		t.Error("place did not round-trip through the list")
	}

	updated := call(t, "PATCH", base+"/"+placeID, alice, map[string]any{
		"name": "The Drained Library", "atmosphere": "dusty",
	}, ifMatch(rev(t, created))).expect(http.StatusOK).json()
	if updated["name"] != "The Drained Library" {
		t.Errorf("name = %v", updated["name"])
	}
	call(t, "PATCH", base+"/"+placeID, alice, map[string]any{"name": "Stale"},
		ifMatch(rev(t, created))).expect(http.StatusConflict)

	if outboxCount(t, id, "place") != 2 {
		t.Errorf("place writes did not enqueue index events")
	}

	call(t, "DELETE", base+"/"+placeID, alice, nil, ifMatch(rev(t, updated))).
		expect(http.StatusNoContent)
	if remaining := get(t, base, alice).expect(http.StatusOK).list(); len(remaining) != 0 {
		t.Errorf("place survived deletion")
	}

	call(t, "POST", base, alice, map[string]any{"name": ""}).expect(http.StatusBadRequest)
	get(t, base, bob).expect(http.StatusForbidden)
}

// ── plot lines and events ───────────────────────────────────────────────────

func TestPlotLineCRUD(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Plots")
	id := story["id"].(string)
	base := worldPath(id, "plots")

	line := newPlotLine(t, alice, id, "The Heist")
	if line["events"] == nil {
		t.Error("events serialized as null, want []")
	}
	lineID := line["id"].(string)

	updated := call(t, "PATCH", base+"/"+lineID, alice, map[string]any{
		"name": "The Heist, Revised", "description": "d2",
	}, ifMatch(rev(t, line))).expect(http.StatusOK).json()
	if updated["name"] != "The Heist, Revised" {
		t.Errorf("name = %v", updated["name"])
	}
	call(t, "PATCH", base+"/"+lineID, alice, map[string]any{"name": "Stale"},
		ifMatch(rev(t, line))).expect(http.StatusConflict)

	// Plot lines carry no indexable prose of their own — only their events do —
	// so they deliberately do not enqueue an index event.
	if n := outboxCount(t, id, "plot_line"); n != 0 {
		t.Errorf("plot lines should not be indexed, got %d events", n)
	}

	call(t, "DELETE", base+"/"+lineID, alice, nil, ifMatch(rev(t, updated))).
		expect(http.StatusNoContent)
	if remaining := get(t, base, alice).expect(http.StatusOK).list(); len(remaining) != 0 {
		t.Errorf("plot line survived deletion")
	}

	call(t, "POST", base, alice, map[string]any{"name": "   "}).expect(http.StatusBadRequest)
	get(t, base, bob).expect(http.StatusForbidden)
}

func TestPlotEventCRUDAndOrdering(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Events")
	id := story["id"].(string)
	line := newPlotLine(t, alice, id, "Main")
	lineID := line["id"].(string)
	events := worldPath(id, "plots") + "/" + lineID + "/events"

	first := newEvent(t, alice, id, lineID, "The summons")
	second := newEvent(t, alice, id, lineID, "The betrayal")
	third := newEvent(t, alice, id, lineID, "The reckoning")

	// Defaults are applied rather than rejected.
	if first["tensionLevel"].(float64) != 5 || first["pacing"] != "moderate" ||
		first["storyBeat"] != "rising_action" {
		t.Errorf("unexpected event defaults: %v", first)
	}
	if first["orderIndex"].(float64) != 0 || third["orderIndex"].(float64) != 2 {
		t.Errorf("events not appended in order: %v, %v",
			first["orderIndex"], third["orderIndex"])
	}

	// Events come back nested inside their line, in position order.
	plots := get(t, worldPath(id, "plots"), alice).expect(http.StatusOK).list()
	names := []string{}
	for _, e := range plots[0]["events"].([]any) {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	want := []string{"The summons", "The betrayal", "The reckoning"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("event order = %v, want %v", names, want)
	}

	// Reordering is guarded by the plot line's revision, not the events'.
	reordered := call(t, "POST", events+"/reorder", alice, map[string]any{
		"orderedIds": []string{
			third["id"].(string), first["id"].(string), second["id"].(string)},
	}, ifMatch(rev(t, line))).expect(http.StatusOK).list()
	got := []string{}
	for _, e := range reordered {
		got = append(got, e["name"].(string))
	}
	wantOrder := []string{"The reckoning", "The summons", "The betrayal"}
	if fmt.Sprint(got) != fmt.Sprint(wantOrder) {
		t.Errorf("reordered = %v, want %v", got, wantOrder)
	}

	// The line's revision has moved on, so the same token cannot reorder twice.
	call(t, "POST", events+"/reorder", alice, map[string]any{
		"orderedIds": []string{first["id"].(string)},
	}, ifMatch(rev(t, line))).expect(http.StatusConflict)

	// Deleting an event renumbers the survivors and returns them.
	current := reordered[0]
	after := call(t, "DELETE", events+"/"+current["id"].(string), alice, nil,
		ifMatch(rev(t, current))).expect(http.StatusOK).list()
	if len(after) != 2 {
		t.Fatalf("expected 2 events after deletion, got %d", len(after))
	}
	for i, e := range after {
		if int(e["orderIndex"].(float64)) != i {
			t.Errorf("positions not contiguous after deletion: %v", after)
		}
	}
}

func TestPlotEventReferencesAreScopedToTheStory(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Refs")
	id := story["id"].(string)
	line := newPlotLine(t, alice, id, "Main")
	lineID := line["id"].(string)
	events := worldPath(id, "plots") + "/" + lineID + "/events"

	cast := newCharacter(t, alice, id, "Protagonist")
	other := newStory(t, bob, "Bob's")
	outsider := newCharacter(t, bob, other["id"].(string), "Outsider")

	ok := call(t, "POST", events, alice, map[string]any{
		"name": "With cast", "content": "c", "characterIds": []string{cast["id"].(string)},
	}).expect(http.StatusCreated).json()
	if len(ok["characterIds"].([]any)) != 1 {
		t.Errorf("characterIds = %v", ok["characterIds"])
	}

	// A character from another story is refused as bad input, not accepted and
	// not a server error.
	call(t, "POST", events, alice, map[string]any{
		"name": "Borrowed cast", "content": "c",
		"characterIds": []string{outsider["id"].(string)},
	}).expect(http.StatusUnprocessableEntity)

	// Out-of-range tension is bad input too.
	call(t, "POST", events, alice, map[string]any{
		"name": "Too tense", "content": "c", "tensionLevel": 99,
	}).expect(http.StatusUnprocessableEntity)

	// A dependency on an event outside the story is refused.
	call(t, "POST", events, alice, map[string]any{
		"name": "Bad dependency", "content": "c",
		"dependencies": []map[string]any{
			{"eventId": "11111111-1111-1111-1111-111111111111",
				"plotLineId": lineID, "relationshipType": "blocks"},
		},
	}).expect(http.StatusUnprocessableEntity)

	// A real dependency inside the story is accepted and read back.
	dependent := call(t, "POST", events, alice, map[string]any{
		"name": "Depends", "content": "c",
		"dependencies": []map[string]any{
			{"eventId": ok["id"], "plotLineId": lineID, "relationshipType": "blocks"},
		},
	}).expect(http.StatusCreated).json()
	if len(dependent["dependencies"].([]any)) != 1 {
		t.Errorf("dependencies = %v", dependent["dependencies"])
	}
}

func TestPlotEventWriteEnqueuesIndexingOutbox(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Indexed events")
	id := story["id"].(string)
	lineID := newPlotLine(t, alice, id, "Main")["id"].(string)

	e := newEvent(t, alice, id, lineID, "Indexed")
	if outboxCount(t, id, "plot_event") != 1 {
		t.Fatal("event create did not enqueue an index event")
	}
	call(t, "DELETE", worldPath(id, "plots")+"/"+lineID+"/events/"+e["id"].(string),
		alice, nil, ifMatch(rev(t, e))).expect(http.StatusOK)
	if n := outboxCount(t, id, "plot_event"); n != 2 {
		t.Errorf("event delete did not enqueue an index event, outbox has %d", n)
	}
}

// ── ids and empty collections ───────────────────────────────────────────────

func TestWorldbuildingRejectsMalformedIDs(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Ids")
	id := story["id"].(string)
	lineID := newPlotLine(t, alice, id, "Main")["id"].(string)

	// A malformed id must not reach the uuid cast, where it becomes a 500.
	call(t, "PATCH", worldPath(id, "characters")+"/not-a-uuid", alice,
		map[string]any{"name": "x"}, ifMatch(1)).expect(http.StatusNotFound)
	call(t, "DELETE", worldPath(id, "characters")+"/not-a-uuid", alice, nil,
		ifMatch(1)).expect(http.StatusNotFound)
	call(t, "PATCH", worldPath(id, "places")+"/not-a-uuid", alice,
		map[string]any{"name": "x"}, ifMatch(1)).expect(http.StatusNotFound)
	call(t, "DELETE", worldPath(id, "places")+"/not-a-uuid", alice, nil,
		ifMatch(1)).expect(http.StatusNotFound)
	call(t, "PATCH", worldPath(id, "plots")+"/not-a-uuid", alice,
		map[string]any{"name": "x"}, ifMatch(1)).expect(http.StatusNotFound)
	call(t, "DELETE", worldPath(id, "plots")+"/not-a-uuid", alice, nil,
		ifMatch(1)).expect(http.StatusNotFound)
	call(t, "POST", worldPath(id, "plots")+"/not-a-uuid/events", alice,
		map[string]any{"name": "x"}).expect(http.StatusNotFound)
	call(t, "PATCH", worldPath(id, "plots")+"/"+lineID+"/events/not-a-uuid", alice,
		map[string]any{"name": "x"}, ifMatch(1)).expect(http.StatusNotFound)
	call(t, "DELETE", worldPath(id, "plots")+"/"+lineID+"/events/not-a-uuid", alice,
		nil, ifMatch(1)).expect(http.StatusNotFound)

	// A well-formed but absent id is also a 404.
	absent := "11111111-1111-1111-1111-111111111111"
	call(t, "PATCH", worldPath(id, "characters")+"/"+absent, alice,
		map[string]any{"name": "x"}, ifMatch(1)).expect(http.StatusNotFound)
	call(t, "POST", worldPath(id, "plots")+"/"+absent+"/events", alice,
		map[string]any{"name": "x"}).expect(http.StatusNotFound)
}

func TestWorldbuildingEmptyCollections(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Empty")
	id := story["id"].(string)

	// Empty collections serialize as [], never null.
	get(t, worldPath(id, "characters"), alice).expect(http.StatusOK).list()
	get(t, worldPath(id, "places"), alice).expect(http.StatusOK).list()
	get(t, worldPath(id, "plots"), alice).expect(http.StatusOK).list()

	// An unknown story is a 404 on every collection.
	absent := "/v1/stories/11111111-1111-1111-1111-111111111111/"
	get(t, absent+"characters", alice).expect(http.StatusNotFound)
	get(t, absent+"places", alice).expect(http.StatusNotFound)
	get(t, absent+"plots", alice).expect(http.StatusNotFound)
}

// Single-entity GET was added for the MCP read port: PATCH and DELETE already
// existed on these paths while GET returned 405.
func TestWorldbuildingSingleEntityGet(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Atlas of Small Rooms")
	sid := story["id"].(string)

	character := newCharacter(t, alice, sid, "Wren")
	got := call(t, "GET", worldPath(sid, "characters")+"/"+character["id"].(string), alice, nil).
		expect(http.StatusOK).json()
	if got["name"] != "Wren" {
		t.Errorf("name = %v, want Wren", got["name"])
	}
	// Relationships must be hydrated, not left nil, as on the list route.
	if _, ok := got["relationships"]; !ok {
		t.Error("expected relationships to be present")
	}

	place := call(t, "POST", worldPath(sid, "places"), alice,
		map[string]any{"name": "The Long Gallery", "description": "d"}).
		expect(http.StatusCreated).json()
	if p := call(t, "GET", worldPath(sid, "places")+"/"+place["id"].(string), alice, nil).
		expect(http.StatusOK).json(); p["name"] != "The Long Gallery" {
		t.Errorf("place name = %v", p["name"])
	}

	line := newPlotLine(t, alice, sid, "Descent")
	if pl := call(t, "GET", worldPath(sid, "plots")+"/"+line["id"].(string), alice, nil).
		expect(http.StatusOK).json(); pl["name"] != "Descent" {
		t.Errorf("plot name = %v", pl["name"])
	}
}

func TestWorldbuildingSingleEntityGetIsOwnerOnly(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Atlas of Small Rooms")
	sid := story["id"].(string)
	character := newCharacter(t, alice, sid, "Wren")

	// Publishing must not open worldbuilding to other readers.
	call(t, "PATCH", "/v1/stories/"+sid, alice, map[string]any{
		"title": "Atlas of Small Rooms", "published": true,
	}, map[string]string{"If-Match": "1"})

	path := worldPath(sid, "characters") + "/" + character["id"].(string)
	if got := call(t, "GET", path, bob, nil); got.Status != http.StatusForbidden && got.Status != http.StatusNotFound {
		t.Errorf("non-owner GET = %d, want 403 or 404", got.Status)
	}
	call(t, "GET", worldPath(sid, "characters")+"/11111111-1111-1111-1111-111111111111", alice, nil).
		expect(http.StatusNotFound)
}
