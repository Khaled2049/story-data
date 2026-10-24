package e2e

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

// Two things settle a competition without a person calling settle: the first
// read after its voting deadline, and the settle-due command. The command tests
// drive it through the store, the way cmd/api runs it.

func dbPhase(t *testing.T, id string) string {
	t.Helper()
	var phase string
	if err := testPool.QueryRow(context.Background(),
		`SELECT phase FROM competitions WHERE id=$1::uuid`, id).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	return phase
}

func dbSettledAt(t *testing.T, id string) time.Time {
	t.Helper()
	var at time.Time
	if err := testPool.QueryRow(context.Background(),
		`SELECT settled_at FROM competitions WHERE id=$1::uuid`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestSettleDueSettlesEachDueCompetitionOnce(t *testing.T) {
	reset(t)
	ctx := context.Background()
	st := store.New(testPool)
	grantInitial(t, bob, carol, dave)

	if ids, err := st.ListDueSettlements(ctx); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("nothing due: ids %v, err %v; want an empty list", ids, err)
	}

	due := openCompetition(t, alice, "Due", tale(10), "0")
	enter(t, bob, due)
	registerVoter(t, dave, due)
	advance(t, alice, due, "voting")
	call(t, "PUT", "/v1/competitions/"+due+"/ballots/me", dave,
		map[string]any{"submissionIds": []string{bob}}).expect(http.StatusNoContent)
	passTime(t, due, "voting_deadline_at")

	notYet := openCompetition(t, alice, "Still voting", tale(10), "0")
	enter(t, carol, notYet)
	advance(t, alice, notYet, "voting")

	done := openCompetition(t, alice, "Already settled", tale(10), "0")
	enter(t, carol, done)
	advance(t, alice, done, "voting")
	passTime(t, done, "voting_deadline_at")
	call(t, "POST", "/v1/competitions/"+done+"/settle", alice, nil).expect(http.StatusOK)
	doneAt := dbSettledAt(t, done)

	draft := newDraft(t, alice, "Never published", tale(10), "0")["id"].(string)
	passTime(t, draft, "deadline_at")
	passTime(t, draft, "voting_deadline_at")

	// Both deadlines passed with no request since, so it still reads "open":
	// the job has to sweep before it can see it. Any competition request
	// would sweep it, so this one is set up last.
	stale := openCompetition(t, alice, "Nobody looked", tale(10), "0")
	enter(t, carol, stale)
	passTime(t, stale, "deadline_at")
	passTime(t, stale, "voting_deadline_at")
	if got := dbPhase(t, stale); got != "open" {
		t.Fatalf("setup: stale competition is %s, want open", got)
	}

	settled, err := st.SettleDue(ctx)
	if err != nil {
		t.Fatalf("settle-due: %v", err)
	}
	slices.Sort(settled)
	want := []string{due, stale}
	slices.Sort(want)
	if !slices.Equal(settled, want) {
		t.Errorf("settled %v, want %v", settled, want)
	}
	for id, phase := range map[string]string{
		due: "settled", stale: "settled", notYet: "voting", done: "settled", draft: "draft",
	} {
		if got := dbPhase(t, id); got != phase {
			t.Errorf("%s: phase %s, want %s", id, got, phase)
		}
	}
	if got := dbSettledAt(t, done); !got.Equal(doneAt) {
		t.Errorf("already-settled competition was settled again: %v then %v", doneAt, got)
	}
	assertBalance(t, userAccount(bob), tale(initialGrantTale+10))
	dueAt := dbSettledAt(t, due)

	again, err := st.SettleDue(ctx)
	if err != nil || len(again) != 0 {
		t.Errorf("second run settled %v, err %v; want nothing", again, err)
	}
	if got := dbSettledAt(t, due); !got.Equal(dueAt) {
		t.Errorf("second run moved settled_at: %v then %v", dueAt, got)
	}
	assertLedgerIntact(t)
}

func TestSettleDueKeepsGoingPastAFailure(t *testing.T) {
	reset(t)
	ctx := context.Background()
	grantInitial(t, bob, carol)

	broken := openCompetition(t, alice, "Escrow gone", tale(10), "0")
	enter(t, bob, broken)
	ok := openCompetition(t, alice, "Fine", tale(10), "0")
	enter(t, carol, ok)
	for _, id := range []string{broken, ok} {
		advance(t, alice, id, "voting")
		passTime(t, id, "voting_deadline_at")
	}
	// Empty the escrow so the payout hits the insufficient-funds guard.
	if _, err := testPool.Exec(ctx,
		`UPDATE token_accounts SET balance=0 WHERE account_id=$1`, escrowAccount(broken)); err != nil {
		t.Fatal(err)
	}

	settled, err := store.New(testPool).SettleDue(ctx)
	if err == nil {
		t.Fatal("settle-due reported no error for a competition it could not settle")
	}
	if !slices.Equal(settled, []string{ok}) {
		t.Errorf("settled %v, want only %s", settled, ok)
	}
	if got := dbPhase(t, broken); got != "voting" {
		t.Errorf("failed competition is %s, want voting so the next run retries it", got)
	}
}

// dueWithAWinner returns a competition in voting, past its voting deadline,
// with one ballot for bob.
func dueWithAWinner(t *testing.T, title string) string {
	t.Helper()
	id := openCompetition(t, alice, title, tale(10), "0")
	grantInitial(t, bob, dave)
	enter(t, bob, id)
	registerVoter(t, dave, id)
	advance(t, alice, id, "voting")
	call(t, "PUT", "/v1/competitions/"+id+"/ballots/me", dave,
		map[string]any{"submissionIds": []string{bob}}).expect(http.StatusNoContent)
	passTime(t, id, "voting_deadline_at")
	return id
}

func TestReadingADueCompetitionSettlesIt(t *testing.T) {
	reset(t)
	id := dueWithAWinner(t, "Read me")

	got := get(t, "/v1/competitions/"+id, "").expect(http.StatusOK).json()
	if got["phase"] != "settled" {
		t.Fatalf("an anonymous read after the deadline left phase %v, want settled", got["phase"])
	}
	results, _ := got["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["userId"] != bob {
		t.Errorf("results = %v, want bob first", got["results"])
	}
	assertBalance(t, userAccount(bob), tale(initialGrantTale+10))
	at := dbSettledAt(t, id)

	get(t, "/v1/competitions/"+id, "").expect(http.StatusOK)
	if again := dbSettledAt(t, id); !again.Equal(at) {
		t.Errorf("a second read settled again: %v then %v", at, again)
	}
	assertLedgerIntact(t)
}

func TestListingSettlesDueCompetitionsOnThePage(t *testing.T) {
	reset(t)
	due := dueWithAWinner(t, "Due on the list")
	grantInitial(t, carol)
	notYet := openCompetition(t, alice, "Still voting", tale(10), "0")
	enter(t, carol, notYet)
	advance(t, alice, notYet, "voting")

	phases := map[string]any{}
	for _, c := range get(t, "/v1/competitions", "").expect(http.StatusOK).list() {
		phases[c["id"].(string)] = c["phase"]
	}
	if phases[due] != "settled" || phases[notYet] != "voting" {
		t.Errorf("listed phases due=%v notYet=%v; want settled and voting",
			phases[due], phases[notYet])
	}
	assertLedgerIntact(t)
}

// Settling is maintenance the reader did not ask for, so a payout that fails
// must not turn their read into an error.
func TestReadStillSucceedsWhenSettlingFails(t *testing.T) {
	reset(t)
	ctx := context.Background()
	id := dueWithAWinner(t, "Escrow gone")
	if _, err := testPool.Exec(ctx,
		`UPDATE token_accounts SET balance=0 WHERE account_id=$1`, escrowAccount(id)); err != nil {
		t.Fatal(err)
	}

	if got := get(t, "/v1/competitions/"+id, "").expect(http.StatusOK).json(); got["phase"] != "voting" {
		t.Fatalf("phase after a failed settle = %v, want voting", got["phase"])
	}

	if _, err := testPool.Exec(ctx,
		`UPDATE token_accounts SET balance=$2::numeric WHERE account_id=$1`,
		escrowAccount(id), tale(10)); err != nil {
		t.Fatal(err)
	}
	if got := get(t, "/v1/competitions/"+id, "").expect(http.StatusOK).json(); got["phase"] != "settled" {
		t.Errorf("the next read did not retry: phase %v", got["phase"])
	}
}
