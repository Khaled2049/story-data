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

func TestPlotTemplateBatchIsAtomicAndAppends(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Template batch")
	storyID := story["id"].(string)
	base := worldPath(storyID, "plots")
	line := newPlotLine(t, alice, storyID, "Existing")
	lineID := line["id"].(string)
	first := newEvent(t, alice, storyID, lineID, "Already here")
	path := base + "/" + lineID + "/events/batch"
	before := outboxCount(t, storyID, "plot_event")
	call(t, "POST", path, alice, map[string]any{"events": []any{
		map[string]any{"name": "Would insert"}, map[string]any{"name": ""},
	}}).expect(http.StatusUnprocessableEntity)
	if got := get(t, base+"/"+lineID, alice).expect(http.StatusOK).json()["events"].([]any); len(got) != 1 {
		t.Fatalf("partial batch left events: %v", got)
	}
	if got := outboxCount(t, storyID, "plot_event"); got != before {
		t.Fatalf("outbox count %d, want %d", got, before)
	}
	created := call(t, "POST", path, alice, map[string]any{"events": []any{
		map[string]any{"name": "Second"}, map[string]any{"name": "Third"},
	}}).expect(http.StatusCreated).list()
	if len(created) != 2 || created[0]["orderIndex"] != float64(1) || created[1]["orderIndex"] != float64(2) {
		t.Fatalf("batch did not append in order: %v", created)
	}
	if first["orderIndex"] != float64(0) {
		t.Fatal("existing event moved")
	}
	tooMany := make([]any, 51)
	for i := range tooMany {
		tooMany[i] = map[string]any{"name": "Overflow"}
	}
	call(t, "POST", path, alice, map[string]any{"events": tooMany}).expect(http.StatusUnprocessableEntity)
	call(t, "POST", base+"/with-events", alice, map[string]any{
		"line":   map[string]any{"name": "Partial line"},
		"events": []any{map[string]any{"name": "Valid"}, map[string]any{"name": ""}},
	}).expect(http.StatusUnprocessableEntity)
	if got := get(t, base, alice).expect(http.StatusOK).list(); len(got) != 1 {
		t.Fatalf("partial line left behind: %v", got)
	}
	withEvents := call(t, "POST", base+"/with-events", alice, map[string]any{
		"line":   map[string]any{"name": "New template"},
		"events": []any{map[string]any{"name": "Setup"}},
	}).expect(http.StatusCreated).json()
	if len(withEvents["events"].([]any)) != 1 {
		t.Fatalf("new line has wrong events: %v", withEvents)
	}
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
	if first["pacing"] != "moderate" || first["storyBeat"] != "rising_action" {
		t.Errorf("unexpected event defaults: %v", first)
	}
	if _, ok := first["tensionLevel"]; ok {
		t.Errorf("events no longer carry a tension level: %v", first)
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

	// Removed fields are unknown fields now.
	for _, removed := range []map[string]any{
		{"tensionLevel": 5},
		{"dependencies": []any{}},
		{"timeConstraint": map[string]any{"timeGap": "later"}},
	} {
		body := map[string]any{"name": "Gone", "content": "c"}
		for k, v := range removed {
			body[k] = v
		}
		call(t, "POST", events, alice, body).expect(http.StatusBadRequest)
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

// An event links to a chapter by id, so reordering chapters keeps the plan
// attached, and deleting the chapter leaves the event unassigned.
func TestPlotEventChapterLinkAndStatus(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Linked")
	id := story["id"].(string)
	lineID := newPlotLine(t, alice, id, "Main")["id"].(string)
	events := worldPath(id, "plots") + "/" + lineID + "/events"
	chapters := "/v1/stories/" + id + "/chapters"
	ch2 := call(t, "POST", chapters, alice, map[string]any{
		"title": "The Ford", "content": "", "position": 1,
	}).expect(http.StatusCreated).json()
	ch2ID := ch2["id"].(string)

	// New events are unassigned and planned.
	plain := newEvent(t, alice, id, lineID, "Unplaced")
	if plain["chapterId"] != nil || plain["status"] != "planned" {
		t.Errorf("defaults: chapterId=%v status=%v", plain["chapterId"], plain["status"])
	}

	linked := call(t, "POST", events, alice, map[string]any{
		"name": "Crossing", "content": "c", "chapterId": ch2ID, "status": "in_progress",
	}).expect(http.StatusCreated).json()
	if linked["chapterId"] != ch2ID || linked["status"] != "in_progress" {
		t.Fatalf("linked event = %v", linked)
	}

	// Moving the chapter to the front does not touch the link.
	call(t, "PATCH", chapters+"/"+ch2ID, alice, map[string]any{
		"title": "The Ford", "content": "", "position": -1,
	}, ifMatch(rev(t, ch2))).expect(http.StatusOK)
	got := get(t, worldPath(id, "plots"), alice).expect(http.StatusOK).list()[0]["events"].([]any)[1].(map[string]any)
	if got["chapterId"] != ch2ID {
		t.Errorf("chapterId after reorder = %v, want %v", got["chapterId"], ch2ID)
	}

	// A chapter from another story, a malformed id and an unknown status are bad input.
	other := newStory(t, bob, "Bob's")
	foreign := get(t, "/v1/stories/"+other["id"].(string)+"/chapters", bob).expect(http.StatusOK).list()[0]["id"]
	for _, body := range []map[string]any{
		{"name": "x", "content": "c", "chapterId": foreign},
		{"name": "x", "content": "c", "chapterId": "not-a-uuid"},
		{"name": "x", "content": "c", "status": "done"},
	} {
		call(t, "POST", events, alice, body).expect(http.StatusUnprocessableEntity)
	}

	// Deleting the chapter unassigns the event and bumps its revision, so the
	// old token can no longer write the deleted chapter back.
	chNow := get(t, chapters+"/"+ch2ID, alice).expect(http.StatusOK).json()
	call(t, "DELETE", chapters+"/"+ch2ID, alice, nil, ifMatch(rev(t, chNow))).expect(http.StatusNoContent)
	after := get(t, worldPath(id, "plots"), alice).expect(http.StatusOK).list()[0]["events"].([]any)[1].(map[string]any)
	if after["chapterId"] != nil {
		t.Errorf("chapterId after chapter delete = %v, want null", after["chapterId"])
	}
	if rev(t, after) != rev(t, linked)+1 {
		t.Errorf("revision after chapter delete = %d, want %d", rev(t, after), rev(t, linked)+1)
	}
	call(t, "PATCH", events+"/"+linked["id"].(string), alice, map[string]any{
		"name": "Crossing", "content": "c", "chapterId": ch2ID,
	}, ifMatch(rev(t, linked))).expect(http.StatusConflict)
	if after["status"] != "in_progress" {
		t.Errorf("status lost on chapter delete: %v", after["status"])
	}
}

// Reordering and deleting only move positions. Event revisions stay put, so a
// content save already in flight for a sibling is not refused with a 409.
func TestPlotEventOrderChangesKeepEventRevisions(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Order")
	id := story["id"].(string)
	line := newPlotLine(t, alice, id, "Main")
	lineID := line["id"].(string)
	events := worldPath(id, "plots") + "/" + lineID + "/events"
	a := newEvent(t, alice, id, lineID, "A")
	b := newEvent(t, alice, id, lineID, "B")
	c := newEvent(t, alice, id, lineID, "C")

	reordered := call(t, "POST", events+"/reorder", alice, map[string]any{
		"orderedIds": []string{c["id"].(string), b["id"].(string), a["id"].(string)},
	}, ifMatch(rev(t, line))).expect(http.StatusOK).list()
	for _, e := range reordered {
		if rev(t, e) != 1 {
			t.Errorf("reorder bumped %v to revision %d", e["name"], rev(t, e))
		}
	}

	after := call(t, "DELETE", events+"/"+c["id"].(string), alice, nil,
		ifMatch(rev(t, c))).expect(http.StatusOK).list()
	for _, e := range after {
		if rev(t, e) != 1 {
			t.Errorf("delete bumped %v to revision %d", e["name"], rev(t, e))
		}
	}

	// A save carrying the revision from before the reorder still lands.
	call(t, "PATCH", events+"/"+a["id"].(string), alice, map[string]any{
		"name": "A, edited", "content": "c",
	}, ifMatch(rev(t, a))).expect(http.StatusOK)
}

// eventsByID flattens a plots response into event id -> event.
func eventsByID(t *testing.T, plots []map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, line := range plots {
		for _, raw := range line["events"].([]any) {
			e := raw.(map[string]any)
			e["lineId"] = line["id"]
			out[e["id"].(string)] = e
		}
	}
	return out
}

// Events in one chapter have their own order across plot lines, separate from
// each line's order. Moving an event into a chapter changes its chapterId, so
// that bumps its revision; reordering inside a chapter does not.
func TestPlotChapterOrder(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Chapter order")
	id := story["id"].(string)
	chapters := "/v1/stories/" + id + "/chapters"
	ch1 := get(t, chapters, alice).expect(http.StatusOK).list()[0]["id"].(string)
	ch2 := call(t, "POST", chapters, alice, map[string]any{
		"title": "Two", "content": "", "position": 1,
	}).expect(http.StatusCreated).json()["id"].(string)
	main := newPlotLine(t, alice, id, "Main")["id"].(string)
	sub := newPlotLine(t, alice, id, "Sub")["id"].(string)
	create := func(line, name string, chapter any) map[string]any {
		return call(t, "POST", worldPath(id, "plots")+"/"+line+"/events", alice,
			map[string]any{"name": name, "content": "c", "chapterId": chapter}).
			expect(http.StatusCreated).json()
	}
	a := create(main, "A", ch1)
	b := create(main, "B", ch1)
	c := create(sub, "C", ch1)
	d := create(sub, "D", ch2)
	u := create(main, "U", nil)

	// New events append to their chapter; unassigned ones have no position.
	for i, e := range []map[string]any{a, b, c} {
		if e["chapterPosition"].(float64) != float64(i) {
			t.Errorf("%v chapterPosition = %v, want %d", e["name"], e["chapterPosition"], i)
		}
	}
	if u["chapterPosition"] != nil {
		t.Errorf("unassigned chapterPosition = %v", u["chapterPosition"])
	}

	order := worldPath(id, "plots") + "/chapter-order"
	ids := func(es ...map[string]any) []string {
		out := []string{}
		for _, e := range es {
			out = append(out, e["id"].(string))
		}
		return out
	}

	// A pure reorder moves positions and leaves revisions alone.
	got := eventsByID(t, call(t, "POST", order, alice, map[string]any{
		"chapterId": ch1, "orderedIds": ids(c, a, b),
	}).expect(http.StatusOK).list())
	for i, e := range []map[string]any{c, a, b} {
		now := got[e["id"].(string)]
		if now["chapterPosition"].(float64) != float64(i) || rev(t, now) != rev(t, e) {
			t.Errorf("%v after reorder: position %v revision %d", e["name"], now["chapterPosition"], rev(t, now))
		}
	}

	// Leaving out an event already in the chapter means a stale view.
	call(t, "POST", order, alice, map[string]any{
		"chapterId": ch1, "orderedIds": ids(c, a),
	}).expect(http.StatusConflict)

	// Events from another chapter and from Unassigned move in at their place,
	// in one call, with a revision bump since their chapterId changed.
	got = eventsByID(t, call(t, "POST", order, alice, map[string]any{
		"chapterId": ch1, "orderedIds": ids(c, d, a, u, b),
	}).expect(http.StatusOK).list())
	for i, e := range []map[string]any{c, d, a, u, b} {
		now := got[e["id"].(string)]
		if now["chapterId"] != ch1 || now["chapterPosition"].(float64) != float64(i) {
			t.Errorf("%v: chapter %v position %v", e["name"], now["chapterId"], now["chapterPosition"])
		}
	}
	if rev(t, got[d["id"].(string)]) != rev(t, d)+1 || rev(t, got[u["id"].(string)]) != rev(t, u)+1 {
		t.Errorf("moved-in events should bump their revision once")
	}
	if rev(t, got[a["id"].(string)]) != rev(t, a) {
		t.Errorf("an event already in the chapter should keep its revision")
	}

	// Bad input: duplicates, a foreign event, a foreign chapter.
	other := newStory(t, bob, "Bob's")
	otherID := other["id"].(string)
	bobLine := newPlotLine(t, bob, otherID, "Theirs")["id"].(string)
	foreign := newEvent(t, bob, otherID, bobLine, "Not yours")
	bobChapter := get(t, "/v1/stories/"+otherID+"/chapters", bob).expect(http.StatusOK).list()[0]["id"]
	all := ids(c, d, a, u, b)
	for _, body := range []map[string]any{
		{"chapterId": ch1, "orderedIds": append(all, a["id"].(string))},
		{"chapterId": ch1, "orderedIds": append(all, foreign["id"].(string))},
		{"chapterId": bobChapter, "orderedIds": []string{}},
	} {
		call(t, "POST", order, alice, body).expect(http.StatusUnprocessableEntity)
	}

	// Assigning a chapter through PATCH appends; unassigning clears the position.
	aNow := got[a["id"].(string)]
	moved := call(t, "PATCH", worldPath(id, "plots")+"/"+main+"/events/"+a["id"].(string), alice,
		map[string]any{"name": "A", "content": "c", "chapterId": ch2}, ifMatch(rev(t, aNow))).
		expect(http.StatusOK).json()
	if moved["chapterPosition"].(float64) != 0 {
		t.Errorf("A in an empty chapter: position %v, want 0", moved["chapterPosition"])
	}
	cleared := call(t, "PATCH", worldPath(id, "plots")+"/"+main+"/events/"+a["id"].(string), alice,
		map[string]any{"name": "A", "content": "c", "chapterId": nil}, ifMatch(rev(t, moved))).
		expect(http.StatusOK).json()
	if cleared["chapterPosition"] != nil {
		t.Errorf("unassigned A kept position %v", cleared["chapterPosition"])
	}

	// Deleting a chapter clears both the link and the position.
	chNow := get(t, chapters+"/"+ch1, alice).expect(http.StatusOK).json()
	call(t, "DELETE", chapters+"/"+ch1, alice, nil, ifMatch(rev(t, chNow))).expect(http.StatusNoContent)
	after := eventsByID(t, get(t, worldPath(id, "plots"), alice).expect(http.StatusOK).list())
	if e := after[c["id"].(string)]; e["chapterId"] != nil || e["chapterPosition"] != nil {
		t.Errorf("after chapter delete: chapter %v position %v", e["chapterId"], e["chapterPosition"])
	}
}

// Moving an event between plot lines renumbers both and leaves the event's
// fields and revision alone.
func TestPlotEventMoveBetweenLines(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Moves")
	id := story["id"].(string)
	main := newPlotLine(t, alice, id, "Main")["id"].(string)
	sub := newPlotLine(t, alice, id, "Sub")["id"].(string)
	a := newEvent(t, alice, id, main, "A")
	b := newEvent(t, alice, id, main, "B")
	c := newEvent(t, alice, id, main, "C")
	newEvent(t, alice, id, sub, "X")
	newEvent(t, alice, id, sub, "Y")
	move := func(from string, e map[string]any) string {
		return worldPath(id, "plots") + "/" + from + "/events/" + e["id"].(string) + "/move"
	}

	plots := call(t, "POST", move(main, b), alice, map[string]any{
		"toPlotLineId": sub, "toIndex": 1,
	}, ifMatch(rev(t, b))).expect(http.StatusOK).list()
	names := func(lineID string) []string {
		out := []string{}
		for _, line := range plots {
			if line["id"] != lineID {
				continue
			}
			for i, raw := range line["events"].([]any) {
				e := raw.(map[string]any)
				if int(e["orderIndex"].(float64)) != i {
					t.Errorf("%v not renumbered: orderIndex %v at %d", e["name"], e["orderIndex"], i)
				}
				out = append(out, e["name"].(string))
			}
		}
		return out
	}
	if got := names(main); fmt.Sprint(got) != "[A C]" {
		t.Errorf("main = %v, want [A C]", got)
	}
	if got := names(sub); fmt.Sprint(got) != "[X B Y]" {
		t.Errorf("sub = %v, want [X B Y]", got)
	}
	byID := eventsByID(t, plots)
	if rev(t, byID[b["id"].(string)]) != rev(t, b) {
		t.Errorf("moving should not bump the event's revision")
	}

	// The event is no longer in Main, and a stale revision is a conflict.
	call(t, "POST", move(main, b), alice, map[string]any{"toPlotLineId": sub, "toIndex": 0},
		ifMatch(rev(t, b))).expect(http.StatusNotFound)
	call(t, "POST", move(main, a), alice, map[string]any{"toPlotLineId": sub, "toIndex": 0},
		ifMatch(rev(t, a)+1)).expect(http.StatusConflict)

	// An index past the end lands last; a foreign target line is refused.
	plots = call(t, "POST", move(main, c), alice, map[string]any{
		"toPlotLineId": sub, "toIndex": 99,
	}, ifMatch(rev(t, c))).expect(http.StatusOK).list()
	if got := names(sub); fmt.Sprint(got) != "[X B Y C]" {
		t.Errorf("sub = %v, want [X B Y C]", got)
	}
	other := newStory(t, bob, "Bob's")
	theirs := newPlotLine(t, bob, other["id"].(string), "Theirs")["id"].(string)
	call(t, "POST", move(main, a), alice, map[string]any{"toPlotLineId": theirs, "toIndex": 0},
		ifMatch(rev(t, a))).expect(http.StatusUnprocessableEntity)
}
