package e2e

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestAdminMembersRequireAdmin(t *testing.T) {
	reset(t)
	call(t, "GET", "/v1/admin/members", "", nil).expect(http.StatusUnauthorized)
	call(t, "GET", "/v1/admin/members", alice, nil).expect(http.StatusForbidden)
	call(t, "GET", "/v1/admin/members", bob, nil, map[string]string{"X-Admin": "true", "X-Service-Token": testServiceToken}).expect(http.StatusForbidden)
	call(t, "POST", "/v1/admin/members", carol, nil, map[string]string{"X-Admin": "true"}).expect(http.StatusMethodNotAllowed)
}

func TestAdminMembersPageSearchAndCountDrafts(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	newProfile(t, alice, "alice_writes", "everyone")
	newProfile(t, bob, "bob_reads", "everyone")
	newProfile(t, carol, "carol_admin", "everyone")
	published := newStory(t, alice, "Released")
	call(t, "PATCH", "/v1/stories/"+published["id"].(string), alice, map[string]any{
		"title": "Released", "authorName": "Alice", "published": true,
	}, ifMatch(1)).expect(http.StatusOK)
	newStory(t, alice, "Private draft")

	var first struct {
		Members []struct {
			UserID              string `json:"userId"`
			StoryCount          int    `json:"storyCount"`
			PublishedStoryCount int    `json:"publishedStoryCount"`
		} `json:"members"`
		NextCursor string `json:"nextCursor"`
	}
	res := get(t, "/v1/admin/members?limit=2", carol, admin).expect(http.StatusOK)
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("member listing must not be cached")
	}
	res.decode(&first)
	if len(first.Members) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	rest := get(t, "/v1/admin/members?limit=2&cursor="+url.QueryEscape(first.NextCursor), carol, admin).expect(http.StatusOK).json()
	if n := len(rest["members"].([]any)); n != 1 || rest["nextCursor"] != nil {
		t.Fatalf("second page = %v", rest)
	}

	found := get(t, "/v1/admin/members?q=ALICE_", carol, admin).expect(http.StatusOK).json()["members"].([]any)
	if len(found) != 1 {
		t.Fatalf("username search = %v", found)
	}
	member := found[0].(map[string]any)
	if member["userId"] != alice || member["storyCount"] != float64(2) || member["publishedStoryCount"] != float64(1) {
		t.Fatalf("alice = %v, want 2 stories with 1 published", member)
	}
	if byID := get(t, "/v1/admin/members?q="+bob, carol, admin).expect(http.StatusOK).json()["members"].([]any); len(byID) != 1 {
		t.Fatalf("user ID search = %v", byID)
	}
	get(t, "/v1/admin/members?cursor=garbage", carol, admin).expect(http.StatusUnprocessableEntity)
}

func TestAdminMemberUsageCountsPerDay(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	path := "/v1/admin/members/" + alice + "/usage?from=2026-10-20&to=2026-10-22"
	call(t, "GET", path, alice, nil).expect(http.StatusForbidden)
	call(t, "GET", path, bob, nil, map[string]string{"X-Admin": "true", "X-Service-Token": testServiceToken}).expect(http.StatusForbidden)

	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO indexing_usage (user_id, day, pass_count) VALUES ('` + alice + `', '2026-10-20', 7), ('` + bob + `', '2026-10-20', 99)`,
		`INSERT INTO recommendations.llm_usage (user_id, day, kind, call_count) VALUES ('` + alice + `', '2026-10-22', 'search', 3), ('` + alice + `', '2026-10-22', 'explain', 2)`,
	} {
		if _, err := testPool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	res := get(t, path, carol, admin).expect(http.StatusOK)
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("member usage must not be cached")
	}
	days := res.json()["days"].([]any)
	if len(days) != 3 {
		t.Fatalf("days = %v, want 3", days)
	}
	first, middle, last := days[0].(map[string]any), days[1].(map[string]any), days[2].(map[string]any)
	if first["indexingPasses"] != float64(7) || middle["indexingPasses"] != float64(0) {
		t.Errorf("indexing = %v / %v, want 7 then 0 (bob's passes are not alice's)", first, middle)
	}
	if last["recsSearches"] != float64(3) || last["recsExplains"] != float64(2) {
		t.Errorf("recs = %v, want 3 searches and 2 explains", last)
	}
	get(t, "/v1/admin/members/"+alice+"/usage?from=2026-01-01&to=2026-10-22", carol, admin).expect(http.StatusUnprocessableEntity)
	get(t, "/v1/admin/members/"+alice+"/other", carol, admin).expect(http.StatusNotFound)
}

func TestAdminBanRefusesTheMemberUntilLifted(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	path := "/v1/admin/members/" + alice + "/ban"
	newProfile(t, alice, "alice_writes", "everyone")
	call(t, "PUT", path, bob, map[string]any{"reason": "spam"}).expect(http.StatusForbidden)
	call(t, "PUT", path, bob, map[string]any{}, map[string]string{"X-Admin": "true", "X-Service-Token": testServiceToken}).expect(http.StatusForbidden)
	call(t, "PUT", "/v1/admin/members/"+carol+"/ban", carol, map[string]any{}, admin).expect(http.StatusUnprocessableEntity)
	call(t, "POST", path, carol, map[string]any{}, admin).expect(http.StatusMethodNotAllowed)

	call(t, "PUT", path, carol, map[string]any{"reason": "spam"}, admin).expect(http.StatusNoContent)
	call(t, "PUT", path, carol, map[string]any{"reason": "repeat"}, admin).expect(http.StatusNoContent)
	get(t, "/v1/stories", alice).expect(http.StatusForbidden)
	// The MCP server's service-token path names the member too, so it is refused as well.
	get(t, "/v1/stories", alice, map[string]string{"X-Service-Token": testServiceToken}).expect(http.StatusForbidden)
	get(t, "/v1/stories", bob).expect(http.StatusOK)

	call(t, "DELETE", path, carol, nil, admin).expect(http.StatusNoContent)
	call(t, "DELETE", path, carol, nil, admin).expect(http.StatusNoContent)
	get(t, "/v1/stories", alice).expect(http.StatusOK)
}
