package e2e

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAdminCompetitionRoutesRequireAdmin(t *testing.T) {
	reset(t)
	id := newDraft(t, alice, "Private contest", "0", "0")["id"].(string)
	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/admin/competitions"},
		{"GET", "/v1/admin/competitions/" + id},
		{"DELETE", "/v1/admin/competitions/" + id},
		{"POST", "/v1/admin/competitions/" + id + "/settle"},
		{"POST", "/v1/admin/competitions/settle-due"},
	} {
		call(t, route.method, route.path, "", nil).expect(http.StatusUnauthorized)
		call(t, route.method, route.path, alice, nil).expect(http.StatusForbidden)
		call(t, route.method, route.path, bob, nil, map[string]string{"X-Admin": "true", "X-Service-Token": testServiceToken}).expect(http.StatusForbidden)
	}
}

func TestAdminCompetitionListingAndDraftReview(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	empty := get(t, "/v1/admin/competitions", carol, admin).expect(http.StatusOK).json()
	if len(empty["competitions"].([]any)) != 0 {
		t.Fatal("expected empty array")
	}
	draft := newDraft(t, alice, "Secret competition", tale(5), "0")["id"].(string)
	published := openCompetition(t, bob, "Public competition", tale(10), "0")
	call(t, "PUT", "/v1/profiles/me", alice, map[string]any{"username": "PrivateHost"}).expect(http.StatusCreated)
	if _, err := testPool.Exec(context.Background(), `UPDATE competitions SET created_at='2026-01-01'`); err != nil {
		t.Fatal(err)
	}
	page := get(t, "/v1/admin/competitions?limit=1", carol, admin).expect(http.StatusOK).json()
	first := page["competitions"].([]any)
	cursor, _ := page["nextCursor"].(string)
	if len(first) != 1 || cursor == "" {
		t.Fatalf("bad first page: %v", page)
	}
	next := get(t, "/v1/admin/competitions?limit=1&cursor="+url.QueryEscape(cursor), carol, admin).expect(http.StatusOK).json()
	second := next["competitions"].([]any)
	if len(second) != 1 || first[0].(map[string]any)["id"] == second[0].(map[string]any)["id"] {
		t.Fatalf("pagination skipped or duplicated: %v", next)
	}
	if next["nextCursor"] != nil {
		t.Fatal("unexpected continuation")
	}
	for query, want := range map[string]string{"phase=draft": draft, "phase=open": published, "q=PRIVATEHOST": draft, "q=" + alice: draft, "q=" + draft: draft} {
		res := get(t, "/v1/admin/competitions?"+query, carol, admin).expect(http.StatusOK)
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("admin response must not be cached")
		}
		items := res.json()["competitions"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["id"] != want {
			t.Fatalf("filter %s: %v", query, items)
		}
		if items[0].(map[string]any)["results"] != nil {
			t.Fatal("unsettled results leaked")
		}
	}
	got := get(t, "/v1/admin/competitions/"+draft, carol, admin).expect(http.StatusOK).json()
	if got["title"] != "Secret competition" || got["creatorId"] != alice {
		t.Fatalf("wrong detail: %v", got)
	}
	// The moderation exception must not relax visibility on reader routes.
	get(t, "/v1/competitions/"+draft, carol, admin).expect(http.StatusNotFound)
	get(t, "/v1/admin/competitions?phase=unknown", carol, admin).expect(http.StatusUnprocessableEntity)
	get(t, "/v1/admin/competitions?cursor=invalid", carol, admin).expect(http.StatusUnprocessableEntity)
	get(t, "/v1/admin/competitions?limit=0", carol, admin).expect(http.StatusBadRequest)
}

func TestAdminCompetitionDeletionIsDraftOnly(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	draft := newDraft(t, alice, "Delete draft", "0", "0")["id"].(string)
	call(t, "DELETE", "/v1/admin/competitions/"+draft, carol, nil, admin).expect(http.StatusNoContent)
	get(t, "/v1/admin/competitions/"+draft, carol, admin).expect(http.StatusNotFound)
	published := openCompetition(t, alice, "Funded contest", tale(10), "0")
	before := get(t, "/v1/me/token-balance", alice).expect(http.StatusOK).json()["balance"]
	call(t, "DELETE", "/v1/admin/competitions/"+published, carol, nil, admin).expect(http.StatusConflict)
	after := get(t, "/v1/me/token-balance", alice).expect(http.StatusOK).json()["balance"]
	if before != after {
		t.Fatal("rejected deletion moved funds")
	}
	get(t, "/v1/admin/competitions/"+published, carol, admin).expect(http.StatusOK)
}

func TestAdminCompetitionReviewAndDeletionAreAudited(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	draft := newDraft(t, alice, "Audited draft", "0", "0")["id"].(string)
	get(t, "/v1/admin/competitions/"+draft, carol, admin).expect(http.StatusOK)
	call(t, "DELETE", "/v1/admin/competitions/"+draft, carol, nil, admin).expect(http.StatusNoContent)
	published := openCompetition(t, alice, "Not deletable", tale(10), "0")
	call(t, "DELETE", "/v1/admin/competitions/"+published, carol, nil, admin).expect(http.StatusConflict)
	if got := auditTrail(t, draft); strings.Join(got, ",") != carol+" competition.view,"+carol+" competition.delete" {
		t.Fatalf("draft audit = %v", got)
	}
	if got := auditTrail(t, published); len(got) != 0 {
		t.Fatalf("refused deletion was audited: %v", got)
	}
}

// Settlement happens on read, so a competition nobody opens after its voting
// deadline stays unsettled. Admins find those with this filter, and their own
// reads must not settle them, or the list would empty itself on sight.
func TestAdminCompetitionsAwaitingSettlement(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	due := dueWithAWinner(t, "Overdue")
	grantInitial(t, carol)
	stillVoting := openCompetition(t, alice, "Still voting", tale(10), "0")
	enter(t, carol, stillVoting)
	advance(t, alice, stillVoting, "voting")
	openCompetition(t, alice, "Still open", tale(10), "0")

	items := get(t, "/v1/admin/competitions?phase=awaiting-settlement", carol, admin).
		expect(http.StatusOK).json()["competitions"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != due {
		t.Fatalf("awaiting settlement = %v, want only %s", items, due)
	}

	get(t, "/v1/admin/competitions/"+due, carol, admin).expect(http.StatusOK)
	if got := dbPhase(t, due); got != "voting" {
		t.Errorf("an admin read settled the competition: phase %s", got)
	}

	get(t, "/v1/competitions/"+due, "").expect(http.StatusOK)
	after := get(t, "/v1/admin/competitions?phase=awaiting-settlement", carol, admin).
		expect(http.StatusOK).json()["competitions"].([]any)
	if len(after) != 0 {
		t.Errorf("a settled competition is still listed: %v", after)
	}
}

// Settling from the admin page still waits for the voting deadline, and every
// settlement it performs is audited with the payout, never on its own.
func TestAdminSettleWaitsForTheDeadlineAndIsAudited(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	id := openCompetition(t, alice, "Admin settles", tale(10), "0")
	grantInitial(t, bob)
	enter(t, bob, id)
	advance(t, alice, id, "voting")
	path := "/v1/admin/competitions/" + id + "/settle"

	call(t, "POST", path, carol, nil, admin).expect(http.StatusUnprocessableEntity)
	if got := auditTrail(t, id); len(got) != 0 {
		t.Fatalf("a refused settle was audited: %v", got)
	}

	passTime(t, id, "voting_deadline_at")
	if got := call(t, "POST", path, carol, nil, admin).expect(http.StatusOK).json(); got["phase"] != "settled" {
		t.Fatalf("phase = %v, want settled", got["phase"])
	}
	call(t, "POST", path, carol, nil, admin).expect(http.StatusOK)
	if got := auditTrail(t, id); strings.Join(got, ",") != carol+" competition.settle" {
		t.Errorf("audit = %v, want one settle by %s", got, carol)
	}

	get(t, path, carol, admin).expect(http.StatusMethodNotAllowed)
	call(t, "POST", "/v1/admin/competitions/"+id+"/unknown", carol, nil, admin).expect(http.StatusNotFound)
	assertLedgerIntact(t)
}

func TestAdminSettleDueReportsWhatItSettledAndWhatFailed(t *testing.T) {
	reset(t)
	admin := map[string]string{"X-Admin": "true"}
	due := dueWithAWinner(t, "Overdue")
	grantInitial(t, carol)
	broken := openCompetition(t, alice, "Escrow gone", tale(10), "0")
	enter(t, carol, broken)
	advance(t, alice, broken, "voting")
	passTime(t, broken, "voting_deadline_at")
	if _, err := testPool.Exec(context.Background(),
		`UPDATE token_accounts SET balance=0 WHERE account_id=$1`, escrowAccount(broken)); err != nil {
		t.Fatal(err)
	}

	res := call(t, "POST", "/v1/admin/competitions/settle-due", carol, nil, admin).expect(http.StatusOK)
	got := res.json()
	if s, f := got["settled"].([]any), got["failed"].([]any); len(s) != 1 || s[0] != due || len(f) != 1 || f[0] != broken {
		t.Fatalf("settle-due = %v, want %s settled and %s failed", got, due, broken)
	}
	if strings.Contains(string(res.Body), "insufficient") || strings.Contains(string(res.Body), "SQLSTATE") {
		t.Errorf("failure detail leaked to the response: %s", res.Body)
	}
	if trail := auditTrail(t, due); strings.Join(trail, ",") != carol+" competition.settle" {
		t.Errorf("settled competition audit = %v", trail)
	}
	if trail := auditTrail(t, broken); len(trail) != 0 {
		t.Errorf("failed settlement was audited: %v", trail)
	}
	if phase := dbPhase(t, broken); phase != "voting" {
		t.Errorf("failed competition is %s, want voting", phase)
	}

	again := call(t, "POST", "/v1/admin/competitions/settle-due", carol, nil, admin).expect(http.StatusOK).json()
	if s := again["settled"].([]any); len(s) != 0 {
		t.Errorf("second run settled %v", s)
	}
	if _, err := testPool.Exec(context.Background(),
		`UPDATE token_accounts SET balance=$2::numeric WHERE account_id=$1`,
		escrowAccount(broken), tale(10)); err != nil {
		t.Fatal(err)
	}
	assertLedgerIntact(t)
}
