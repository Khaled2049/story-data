package e2e

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAdminStoryRoutesRequireAdmin(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Private manuscript")
	id := story["id"].(string)
	chapter := get(t, "/v1/stories/"+id+"/chapters", alice).expect(http.StatusOK).list()[0]["id"].(string)
	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/admin/stories"},
		{"GET", "/v1/admin/stories/" + id},
		{"GET", "/v1/admin/stories/" + id + "/chapters"},
		{"GET", "/v1/admin/stories/" + id + "/chapters/" + chapter},
		{"DELETE", "/v1/admin/stories/" + id},
	} {
		call(t, route.method, route.path, "", nil, ifMatch(1)).expect(http.StatusUnauthorized)
		// Ownership does not grant access to moderation routes.
		call(t, route.method, route.path, alice, nil, ifMatch(1)).expect(http.StatusForbidden)
		call(t, route.method, route.path, bob, nil, ifMatch(1)).expect(http.StatusForbidden)
		// The backend service token cannot assert admin privilege.
		call(t, route.method, route.path, bob, nil, map[string]string{
			"X-Service-Token": testServiceToken, "X-Admin": "true", "If-Match": "1",
		}).expect(http.StatusForbidden)
	}
}

func TestAdminStoryListingPagesAndFiltersAcrossOwners(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	empty := get(t, "/v1/admin/stories", carol, admin).expect(http.StatusOK).json()
	if len(empty["stories"].([]any)) != 0 {
		t.Fatalf("expected empty page: %v", empty)
	}
	draft := newStory(t, alice, "Unreleased manuscript")
	ongoing := newStory(t, bob, "Ocean voyage")
	finished := newStory(t, alice, "Finished voyage")
	for _, story := range []map[string]any{ongoing, finished} {
		call(t, "PATCH", "/v1/stories/"+story["id"].(string), story["ownerId"].(string), map[string]any{
			"title": story["title"], "authorName": "Voyager", "published": true,
		}, ifMatch(1)).expect(http.StatusOK)
	}
	call(t, "PUT", "/v1/stories/"+finished["id"].(string)+"/reader-update", alice, map[string]any{"completionStatus": "completed"}).expect(http.StatusOK)
	// Tie all timestamps so pagination must use the ID as well.
	if _, err := testPool.Exec(context.Background(), `UPDATE stories SET updated_at='2026-01-01'`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 3; i++ {
		page := get(t, "/v1/admin/stories?limit=1&cursor="+url.QueryEscape(cursor), carol, admin).expect(http.StatusOK).json()
		items := page["stories"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected one row: %v", page)
		}
		item := items[0].(map[string]any)
		id := item["id"].(string)
		if seen[id] {
			t.Fatal("duplicate story across pages")
		}
		seen[id] = true
		if item["chapterCount"] != float64(1) {
			t.Fatalf("missing chapter count: %v", item)
		}
		cursor, _ = page["nextCursor"].(string)
		if (i < 2) == (cursor == "") {
			t.Fatalf("wrong cursor: %v", page)
		}
	}
	for status, want := range map[string]string{"draft": draft["id"].(string), "ongoing": ongoing["id"].(string), "completed": finished["id"].(string)} {
		page := get(t, "/v1/admin/stories?status="+status, carol, admin).expect(http.StatusOK).json()
		items := page["stories"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["id"] != want {
			t.Fatalf("status %s: %v", status, page)
		}
	}
	for _, search := range []string{"OCEAN", bob, ongoing["id"].(string)} {
		page := get(t, "/v1/admin/stories?q="+url.QueryEscape(search), carol, admin).expect(http.StatusOK).json()
		items := page["stories"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["id"] != ongoing["id"] {
			t.Fatalf("search %s: %v", search, page)
		}
	}
	get(t, "/v1/admin/stories?cursor=invalid", carol, admin).expect(http.StatusUnprocessableEntity)
	get(t, "/v1/admin/stories?status=unknown", carol, admin).expect(http.StatusUnprocessableEntity)
	get(t, "/v1/admin/stories?limit=0", carol, admin).expect(http.StatusBadRequest)
}

func TestAdminCanReadDraftContentAndDeleteWithRevision(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	story := newStory(t, alice, "Draft to review")
	id := story["id"].(string)
	path := "/v1/admin/stories/" + id
	chapterID := get(t, "/v1/stories/"+id+"/chapters", alice).expect(http.StatusOK).list()[0]["id"].(string)
	content := "<p>Private chapter for moderation.</p>"
	call(t, "PATCH", "/v1/stories/"+id+"/chapters/"+chapterID, alice, map[string]any{"title": "Draft chapter", "content": content}, ifMatch(1)).expect(http.StatusOK)
	res := get(t, path, carol, admin).expect(http.StatusOK)
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("private moderation response must not be cached")
	}
	if res.json()["ownerId"] != alice {
		t.Fatal("incorrect owner")
	}
	index := get(t, path+"/chapters", carol, admin).expect(http.StatusOK).list()
	if len(index) != 1 || index[0]["content"] != "" {
		t.Fatalf("index must include drafts without content: %v", index)
	}
	chapter := get(t, path+"/chapters/"+chapterID, carol, admin).expect(http.StatusOK).json()
	if chapter["content"] != content {
		t.Fatalf("draft content unavailable: %v", chapter)
	}
	// Admin claims confer no new privileges on the normal reader endpoints.
	get(t, "/v1/stories/"+id, carol, admin).expect(http.StatusForbidden)
	get(t, "/v1/stories/"+id+"/chapters/"+chapterID, carol, admin).expect(http.StatusForbidden)
	other := newStory(t, bob, "Other book")
	get(t, "/v1/admin/stories/"+other["id"].(string)+"/chapters/"+chapterID, carol, admin).expect(http.StatusNotFound)
	call(t, "DELETE", path, carol, nil, admin).expect(http.StatusPreconditionRequired)
	call(t, "PATCH", "/v1/stories/"+id, alice, map[string]any{"title": "Updated during review"}, ifMatch(1)).expect(http.StatusOK)
	call(t, "DELETE", path, carol, nil, admin, ifMatch(1)).expect(http.StatusConflict)
	get(t, path, carol, admin).expect(http.StatusOK)
	call(t, "DELETE", path, carol, nil, admin, ifMatch(2)).expect(http.StatusNoContent)
	get(t, path, carol, admin).expect(http.StatusNotFound)
	var chapters, outbox int
	if err := testPool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM chapters WHERE story_id=$1),(SELECT count(*) FROM indexing_outbox WHERE story_id=$1)`, id).Scan(&chapters, &outbox); err != nil {
		t.Fatal(err)
	}
	if chapters != 0 || outbox != 0 {
		t.Fatalf("deletion left chapters=%d outbox=%d", chapters, outbox)
	}
}

func TestAdminDeletionPreservesCompetitionGuard(t *testing.T) {
	reset(t)
	competitionID := openCompetition(t, alice, "Active contest", tale(10), "0")
	id := enter(t, bob, competitionID)
	call(t, "DELETE", "/v1/admin/stories/"+id, carol, nil, map[string]string{"X-Admin": "true"}, ifMatch(1)).expect(http.StatusConflict)
	get(t, "/v1/stories/"+id, bob).expect(http.StatusOK)
}

// auditTrail returns "actor action" rows for a target, oldest first.
func auditTrail(t *testing.T, target string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT actor_id||' '||action FROM admin_audit WHERE target_id=$1 ORDER BY id`, target)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			t.Fatal(err)
		}
		out = append(out, x)
	}
	return out
}

func TestAdminStoryReadsAndDeletionAreAudited(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	id := newStory(t, alice, "Audited")["id"].(string)
	chapterID := get(t, "/v1/stories/"+id+"/chapters", alice).expect(http.StatusOK).list()[0]["id"].(string)
	path := "/v1/admin/stories/" + id
	get(t, path, carol, admin).expect(http.StatusOK)
	get(t, path+"/chapters/"+chapterID, carol, admin).expect(http.StatusOK)
	// A refused deletion leaves no record, since the row shares its transaction.
	call(t, "DELETE", path, carol, nil, admin, ifMatch(9)).expect(http.StatusConflict)
	call(t, "DELETE", path, carol, nil, admin, ifMatch(1)).expect(http.StatusNoContent)
	got := append(auditTrail(t, id), auditTrail(t, chapterID)...)
	want := []string{carol + " story.view", carol + " story.delete", carol + " chapter.view"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit = %v, want %v", got, want)
	}
}
