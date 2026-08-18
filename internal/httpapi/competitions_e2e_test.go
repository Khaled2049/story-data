package httpapi_test

// Competitions and the TALE token ledger.
//
// This domain moves real prize money through a hand-rolled double-entry
// ledger, so the assertions here are as much about conservation as they are
// about HTTP status codes: after every scenario the journal must sum to zero
// per transfer, and every materialized account balance must equal the sum of
// its postings. A status-code-only test would pass while tokens were being
// minted or destroyed.

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	carol = "user-carol"
	dave  = "user-dave"

	// Balances are 18-decimal fixed point, so a "whole" TALE is 1e18.
	initialGrantTale = 1000
	faucetGrantTale  = 250
)

// tale renders a whole-token count as the 18-decimal integer string the API
// returns, so tests can be written in units a human can check.
func tale(n int64) string { return fmt.Sprintf("%d000000000000000000", n) }

func platformAccount() string        { return "platform:treasury" }
func userAccount(uid string) string  { return "user:" + uid }
func escrowAccount(id string) string { return "escrow:competition:" + id }

// ── helpers ─────────────────────────────────────────────────────────────────

// grantInitial materializes a user's opening balance. The grant is lazy — it
// is only created by the code paths that call initialGrant — so tests that
// need a funded user must ask for the balance first.
func grantInitial(t *testing.T, uids ...string) {
	t.Helper()
	for _, uid := range uids {
		get(t, "/v1/me/token-balance", uid).expect(http.StatusOK)
	}
}

// newDraft creates a competition draft whose window is already open: it began
// an hour ago, closes in an hour, and voting closes an hour after that. That
// lets publish land straight in "open" without an extra advance call.
func newDraft(t *testing.T, uid, title string, prize, fee string) map[string]any {
	t.Helper()
	now := time.Now().UTC()
	return call(t, "POST", "/v1/competition-drafts", uid, map[string]any{
		"title":       title,
		"description": "d",
		"category":    "flash-fiction",
		"tags":        []string{"x"},
		"startDate":   now.Add(-time.Hour).Format(time.RFC3339),
		"deadline":    now.Add(time.Hour).Format(time.RFC3339),
		"prizeAmount": prize,
		"entryFee":    fee,
		"creatorName": "Alice",

		"votingDeadline": now.Add(2 * time.Hour).Format(time.RFC3339),
	}).expect(http.StatusCreated).json()
}

// openCompetition drafts and publishes in one step, returning the id. The
// creator's balance is materialized first because PublishCompetition escrows
// the prize without granting.
func openCompetition(t *testing.T, uid, title, prize, fee string) string {
	t.Helper()
	grantInitial(t, uid)
	draft := newDraft(t, uid, title, prize, fee)
	id := draft["id"].(string)
	published := call(t, "POST", "/v1/competition-publish", uid,
		map[string]any{"competitionId": id}).expect(http.StatusOK).json()
	if published["phase"] != "open" {
		t.Fatalf("expected a competition starting in the past to publish as open, got %v",
			published["phase"])
	}
	return id
}

// enter joins the competition and submits a story, the two-step the API
// requires before a user counts as a contestant.
func enter(t *testing.T, uid, competitionID string) string {
	t.Helper()
	story := newStory(t, uid, uid+"'s entry")
	call(t, "PUT", "/v1/competitions/"+competitionID+"/join", uid, nil).
		expect(http.StatusNoContent)
	call(t, "POST", "/v1/competitions/"+competitionID+"/submissions/me", uid,
		map[string]any{"storyId": story["id"]}).expect(http.StatusNoContent)
	return story["id"].(string)
}

// advance drives a competition to the phase the clock has made due. The
// endpoint performs only time-implied transitions — the ones that move money
// have their own endpoints — so a test that wants entries closed before the
// deadline moves the deadline first, exactly as a host does through PATCH.
// registerVoter makes uid eligible to cast a ballot. A ballot now requires
// having joined the competition — registration closes when entries do — and a
// public profile old enough that accounts minted for one contest cannot swing
// it. Call this while the competition is still open.
func registerVoter(t *testing.T, uid, competitionID string) {
	t.Helper()
	call(t, "PUT", "/v1/competitions/"+competitionID+"/join", uid, nil).
		expect(http.StatusNoContent)
	agedProfile(t, uid)
}

// agedProfile gives uid a public profile and backdates it past the voting age
// gate, standing in for an account that has been on the platform a while.
func agedProfile(t *testing.T, uid string) {
	t.Helper()
	call(t, "PUT", "/v1/profiles/me", uid,
		map[string]any{"username": strings.ReplaceAll(uid, "-", "_")}).
		expect(http.StatusCreated)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE public_profiles SET created_at=now()-interval '30 days' WHERE user_id=$1`,
		uid); err != nil {
		t.Fatalf("backdate profile for %s: %v", uid, err)
	}
}

func advance(t *testing.T, uid, competitionID, phase string) {
	t.Helper()
	if phase == "voting" {
		call(t, "PATCH", "/v1/competitions/"+competitionID, uid, map[string]any{
			"deadline": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		}).expect(http.StatusOK)
	}
	got := call(t, "POST", "/v1/competitions/"+competitionID+"/advance", uid,
		map[string]any{"targetPhase": phase}).expect(http.StatusOK).json()
	if got["phase"] != phase {
		t.Fatalf("advance to %s left phase %v", phase, got["phase"])
	}
}

// dbBalance reads an account straight from the table, without triggering the
// lazy initial grant that the balance endpoint performs.
func dbBalance(t *testing.T, account string) string {
	t.Helper()
	var b string
	if err := testPool.QueryRow(context.Background(),
		`SELECT COALESCE((SELECT balance::text FROM token_accounts WHERE account_id=$1), '0')`,
		account).Scan(&b); err != nil {
		t.Fatalf("read balance for %s: %v", account, err)
	}
	return b
}

func assertBalance(t *testing.T, account, want string) {
	t.Helper()
	if got := dbBalance(t, account); got != want {
		t.Errorf("balance of %s = %s, want %s", account, got, want)
	}
}

// assertLedgerIntact checks the two invariants the whole design rests on:
// every transfer is zero-sum, and each account's stored balance equals the
// sum of its journal entries. Both live only in Go today, so nothing but a
// test like this catches a drift between the journal and the balances.
func assertLedgerIntact(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	var unbalanced int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT transfer_key FROM ledger_postings
			GROUP BY transfer_key HAVING sum(delta) <> 0
		) t`).Scan(&unbalanced); err != nil {
		t.Fatalf("zero-sum check: %v", err)
	}
	if unbalanced != 0 {
		t.Errorf("%d ledger transfers do not sum to zero", unbalanced)
	}

	// system: accounts are the mint and deliberately have no row, so they are
	// excluded — everything else must reconcile.
	rows, err := testPool.Query(ctx, `
		SELECT p.account_id, sum(p.delta)::text,
		       COALESCE((SELECT a.balance::text FROM token_accounts a
		                 WHERE a.account_id = p.account_id), 'missing')
		FROM ledger_postings p
		WHERE p.account_id NOT LIKE 'system:%'
		GROUP BY p.account_id`)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var account, journal, balance string
		if err := rows.Scan(&account, &journal, &balance); err != nil {
			t.Fatal(err)
		}
		if journal != balance {
			t.Errorf("account %s: journal sums to %s but balance is %s",
				account, journal, balance)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// ── token ledger ────────────────────────────────────────────────────────────

func TestTokenBalanceGrantsInitialAllowanceExactlyOnce(t *testing.T) {
	reset(t)

	first := get(t, "/v1/me/token-balance", alice).expect(http.StatusOK).json()
	if first["balance"] != tale(initialGrantTale) {
		t.Errorf("opening balance = %v, want %s", first["balance"], tale(initialGrantTale))
	}
	if first["symbol"] != "TALE" || first["decimals"].(float64) != 18 {
		t.Errorf("unexpected asset metadata: %v", first)
	}

	// Re-reading the balance must not re-grant.
	second := get(t, "/v1/me/token-balance", alice).expect(http.StatusOK).json()
	if second["balance"] != first["balance"] {
		t.Errorf("balance changed on a second read: %v then %v",
			first["balance"], second["balance"])
	}

	var grants int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM ledger_transfers WHERE reason='grant:initial'`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 1 {
		t.Errorf("expected exactly 1 initial grant, got %d", grants)
	}
	assertLedgerIntact(t)
}

func TestTokenFaucetIsIdempotentWithinTheDay(t *testing.T) {
	reset(t)
	grantInitial(t, alice)

	claimed := call(t, "POST", "/v1/me/token-faucet", alice, nil).
		expect(http.StatusOK).json()
	if claimed["balance"] != tale(initialGrantTale+faucetGrantTale) {
		t.Errorf("after faucet = %v, want %s",
			claimed["balance"], tale(initialGrantTale+faucetGrantTale))
	}

	// The idempotency key is scoped to the UTC day, so a second claim is a
	// no-op rather than a second 250 TALE.
	again := call(t, "POST", "/v1/me/token-faucet", alice, nil).
		expect(http.StatusOK).json()
	if again["balance"] != claimed["balance"] {
		t.Errorf("faucet paid twice in one day: %v then %v",
			claimed["balance"], again["balance"])
	}
	assertLedgerIntact(t)
}

// The admin grant is the platform's only mint besides the opening balance and
// the faucet, so the tests here are mostly about who may call it and what
// happens when the same grant arrives twice.
func TestAdminTokenGrant(t *testing.T) {
	reset(t)
	grants := "/v1/admin/token-grants"
	admin := map[string]string{"X-Admin": "true"}

	granted := call(t, "POST", grants, alice, map[string]any{
		"userId": bob, "amount": tale(500), "idempotencyKey": "support-ticket-1",
	}, admin).expect(http.StatusOK).json()
	// The recipient's opening balance is materialized first, so the grant adds
	// to it rather than replacing it.
	if granted["balance"] != tale(initialGrantTale+500) {
		t.Errorf("balance = %v, want %s", granted["balance"], tale(initialGrantTale+500))
	}

	// Replaying the same key is a no-op, not a second payment.
	again := call(t, "POST", grants, alice, map[string]any{
		"userId": bob, "amount": tale(500), "idempotencyKey": "support-ticket-1",
	}, admin).expect(http.StatusOK).json()
	if again["balance"] != granted["balance"] {
		t.Errorf("a replayed grant paid twice: %v then %v", granted["balance"], again["balance"])
	}

	// A different key is a different grant.
	call(t, "POST", grants, alice, map[string]any{
		"userId": bob, "amount": tale(100), "idempotencyKey": "support-ticket-2",
	}, admin).expect(http.StatusOK)
	assertBalance(t, userAccount(bob), tale(initialGrantTale+600))

	// Minting still has to balance: the mint is a system account, so the
	// journal must show the negative side even though no row holds it.
	assertLedgerIntact(t)

	// Admin only, and authenticated.
	call(t, "POST", grants, carol, map[string]any{
		"userId": bob, "amount": tale(1), "idempotencyKey": "not-admin",
	}).expect(http.StatusForbidden)
	call(t, "POST", grants, "", map[string]any{
		"userId": bob, "amount": tale(1), "idempotencyKey": "anon",
	}, admin).expect(http.StatusUnauthorized)

	// Validation: a grant needs a recipient, a key, and a positive amount.
	for _, bad := range []map[string]any{
		{"userId": "", "amount": tale(1), "idempotencyKey": "k"},
		{"userId": bob, "amount": tale(1), "idempotencyKey": "  "},
		{"userId": bob, "amount": "0", "idempotencyKey": "k"},
		{"userId": bob, "amount": "-5", "idempotencyKey": "k"},
		{"userId": bob, "amount": "abc", "idempotencyKey": "k"},
	} {
		call(t, "POST", grants, alice, bad, admin).expect(http.StatusUnprocessableEntity)
	}

	call(t, "GET", grants, alice, nil, admin).expect(http.StatusMethodNotAllowed)
}

func TestTokenBalanceRequiresAuth(t *testing.T) {
	reset(t)
	get(t, "/v1/me/token-balance", "").expect(http.StatusUnauthorized)
	call(t, "POST", "/v1/me/token-faucet", "", nil).expect(http.StatusUnauthorized)
}

// ── drafts and publishing ───────────────────────────────────────────────────

func TestCompetitionDraftIsPrivateUntilPublished(t *testing.T) {
	reset(t)
	grantInitial(t, alice)

	draft := newDraft(t, alice, "The Vellum Prize", tale(100), tale(10))
	if draft["phase"] != "draft" || draft["published"] != false {
		t.Errorf("new draft should be an unpublished draft, got %v", draft)
	}

	// Drafts are excluded from the public listing but visible to their owner.
	if listed := get(t, "/v1/competitions", alice).expect(http.StatusOK).list(); len(listed) != 0 {
		t.Errorf("draft leaked into the public list: %d entries", len(listed))
	}
	mine := get(t, "/v1/me/competitions/drafts", alice).expect(http.StatusOK).list()
	if len(mine) != 1 {
		t.Fatalf("owner should see 1 draft, got %d", len(mine))
	}
	// Another user's drafts are their own.
	if theirs := get(t, "/v1/me/competitions/drafts", bob).expect(http.StatusOK).list(); len(theirs) != 0 {
		t.Errorf("bob should see no drafts, got %d", len(theirs))
	}

	call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": draft["id"]}).expect(http.StatusOK)

	if listed := get(t, "/v1/competitions", bob).expect(http.StatusOK).list(); len(listed) != 1 {
		t.Errorf("published competition should be publicly listed, got %d", len(listed))
	}
}

func TestCompetitionDraftValidation(t *testing.T) {
	reset(t)

	call(t, "POST", "/v1/competition-drafts", alice,
		map[string]any{"title": "   ", "tags": []string{}}).
		expect(http.StatusUnprocessableEntity)

	// A negative or non-numeric prize is rejected before it can reach the ledger.
	for _, bad := range []string{"-1", "abc", "1.5"} {
		call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
			"title": "Bad money", "tags": []string{}, "prizeAmount": bad,
		}).expect(http.StatusUnprocessableEntity)
	}

	call(t, "POST", "/v1/competition-drafts", "",
		map[string]any{"title": "x", "tags": []string{}}).expect(http.StatusUnauthorized)
}

func TestCompetitionPublishRequiresOrderedDatesAndAPrize(t *testing.T) {
	reset(t)
	grantInitial(t, alice)
	now := time.Now().UTC()

	// No dates at all.
	bare := call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
		"title": "Undated", "tags": []string{}, "prizeAmount": tale(10),
	}).expect(http.StatusCreated).json()
	call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": bare["id"]}).expect(http.StatusUnprocessableEntity)

	// Voting closing before the deadline is out of order.
	backwards := call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
		"title": "Backwards", "tags": []string{}, "prizeAmount": tale(10),
		"startDate":      now.Format(time.RFC3339),
		"deadline":       now.Add(2 * time.Hour).Format(time.RFC3339),
		"votingDeadline": now.Add(time.Hour).Format(time.RFC3339),
	}).expect(http.StatusCreated).json()
	call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": backwards["id"]}).expect(http.StatusUnprocessableEntity)

	// A zero prize means there is nothing to escrow and nothing to win.
	free := newDraft(t, alice, "No prize", "0", "0")
	call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": free["id"]}).expect(http.StatusUnprocessableEntity)

	// None of the rejected publishes moved money.
	assertBalance(t, userAccount(alice), tale(initialGrantTale))
	assertLedgerIntact(t)
}

func TestCompetitionPublishEscrowsThePrizeFromTheCreator(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "The Vellum Prize", tale(100), tale(10))

	assertBalance(t, userAccount(alice), tale(initialGrantTale-100))
	assertBalance(t, escrowAccount(id), tale(100))
	assertLedgerIntact(t)
}

func TestCompetitionPublishRefusesInsufficientBalance(t *testing.T) {
	reset(t)
	grantInitial(t, alice)

	// More than the opening allowance.
	draft := newDraft(t, alice, "Too rich", tale(initialGrantTale+1), "0")
	call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": draft["id"]}).
		expect(http.StatusUnprocessableEntity)

	got := get(t, "/v1/competitions/"+draft["id"].(string), alice).
		expect(http.StatusOK).json()
	if got["phase"] != "draft" {
		t.Errorf("a failed escrow must leave the competition in draft, got %v", got["phase"])
	}
	assertBalance(t, userAccount(alice), tale(initialGrantTale))
	assertLedgerIntact(t)
}

func TestCompetitionPublishIsCreatorOnly(t *testing.T) {
	reset(t)
	grantInitial(t, alice, bob)
	draft := newDraft(t, alice, "Alice's prize", tale(100), "0")

	call(t, "POST", "/v1/competition-publish", bob,
		map[string]any{"competitionId": draft["id"]}).expect(http.StatusForbidden)
	// Bob's balance is untouched — the escrow must never be charged to the
	// caller rather than the creator.
	assertBalance(t, userAccount(bob), tale(initialGrantTale))
	assertBalance(t, escrowAccount(draft["id"].(string)), "0")
}

// ── joining and submitting ──────────────────────────────────────────────────

func TestCompetitionJoinRespectsMaxParticipants(t *testing.T) {
	reset(t)
	grantInitial(t, alice)
	now := time.Now().UTC()
	draft := call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
		"title": "Two seats", "tags": []string{}, "prizeAmount": tale(10),
		"maxParticipants": 1,
		"startDate":       now.Add(-time.Hour).Format(time.RFC3339),
		"deadline":        now.Add(time.Hour).Format(time.RFC3339),
		"votingDeadline":  now.Add(2 * time.Hour).Format(time.RFC3339),
	}).expect(http.StatusCreated).json()
	id := draft["id"].(string)
	call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": id}).expect(http.StatusOK)

	call(t, "PUT", "/v1/competitions/"+id+"/join", bob, nil).expect(http.StatusNoContent)
	// Re-joining is idempotent and must not consume the second seat.
	call(t, "PUT", "/v1/competitions/"+id+"/join", bob, nil).expect(http.StatusNoContent)
	call(t, "PUT", "/v1/competitions/"+id+"/join", carol, nil).
		expect(http.StatusUnprocessableEntity)

	got := get(t, "/v1/competitions/"+id, bob).expect(http.StatusOK).json()
	if got["participants"].(float64) != 1 {
		t.Errorf("participants = %v, want 1", got["participants"])
	}
	if got["isJoined"] != true {
		t.Errorf("isJoined should be true for a participant")
	}
	if seen := get(t, "/v1/competitions/"+id, carol).expect(http.StatusOK).json(); seen["isJoined"] != false {
		t.Errorf("isJoined should be false for a non-participant")
	}
}

func TestCompetitionSubmitRequiresJoinAndStoryOwnership(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Gated", tale(100), "0")
	grantInitial(t, bob, carol)

	bobStory := newStory(t, bob, "Bob's entry")
	submit := "/v1/competitions/" + id + "/submissions/me"

	// Submitting before joining is refused.
	call(t, "POST", submit, bob, map[string]any{"storyId": bobStory["id"]}).
		expect(http.StatusForbidden)

	call(t, "PUT", "/v1/competitions/"+id+"/join", bob, nil).expect(http.StatusNoContent)
	call(t, "PUT", "/v1/competitions/"+id+"/join", carol, nil).expect(http.StatusNoContent)

	// Carol cannot enter a story she does not own.
	call(t, "POST", submit, carol, map[string]any{"storyId": bobStory["id"]}).
		expect(http.StatusForbidden)

	// An unknown story is a 404, not a 500.
	call(t, "POST", submit, bob,
		map[string]any{"storyId": "11111111-1111-1111-1111-111111111111"}).
		expect(http.StatusNotFound)

	call(t, "POST", submit, bob, map[string]any{"storyId": bobStory["id"]}).
		expect(http.StatusNoContent)
	// A second submission while one stands is a conflict.
	call(t, "POST", submit, bob, map[string]any{"storyId": bobStory["id"]}).
		expect(http.StatusConflict)

	subs := get(t, "/v1/competitions/"+id+"/submissions", "").expect(http.StatusOK).list()
	if len(subs) != 1 {
		t.Fatalf("expected 1 submission, got %d", len(subs))
	}
	if subs[0]["userId"] != bob || subs[0]["storyTitle"] != "Bob's entry" {
		t.Errorf("unexpected submission: %v", subs[0])
	}
}

func TestCompetitionSubmitIsRefusedOutsideTheOpenPhase(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Closing", tale(100), "0")
	grantInitial(t, bob)
	story := newStory(t, bob, "Late entry")
	call(t, "PUT", "/v1/competitions/"+id+"/join", bob, nil).expect(http.StatusNoContent)

	advance(t, alice, id, "voting")
	call(t, "POST", "/v1/competitions/"+id+"/submissions/me", bob,
		map[string]any{"storyId": story["id"]}).expect(http.StatusForbidden)
}

func TestCompetitionEntryFeeIsEscrowedAndRefundedOnWithdraw(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Paid entry", tale(100), tale(10))
	grantInitial(t, bob)

	enter(t, bob, id)
	assertBalance(t, userAccount(bob), tale(initialGrantTale-10))
	assertBalance(t, escrowAccount(id), tale(110)) // prize + one entry fee

	got := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if got["entryFeesHeld"] != tale(10) {
		t.Errorf("entryFeesHeld = %v, want %s", got["entryFeesHeld"], tale(10))
	}
	if got["submissionCount"].(float64) != 1 {
		t.Errorf("submissionCount = %v, want 1", got["submissionCount"])
	}

	call(t, "DELETE", "/v1/competitions/"+id+"/submissions/me", bob, nil).
		expect(http.StatusNoContent)

	assertBalance(t, userAccount(bob), tale(initialGrantTale))
	assertBalance(t, escrowAccount(id), tale(100))

	after := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if after["entryFeesHeld"] != "0" {
		t.Errorf("entryFeesHeld after withdraw = %v, want 0", after["entryFeesHeld"])
	}
	if after["submissionCount"].(float64) != 0 {
		t.Errorf("submissionCount after withdraw = %v, want 0", after["submissionCount"])
	}
	if subs := get(t, "/v1/competitions/"+id+"/submissions", "").expect(http.StatusOK).list(); len(subs) != 0 {
		t.Errorf("withdrawn submission still listed: %v", subs)
	}
	assertLedgerIntact(t)
}

// Re-entering after a withdrawal must charge the entry fee again. The refund
// returned the money, so the second submission is a genuinely new charge — but
// the fee transfer is keyed "escrow:entry:{competition}:{user}", which the
// ledger has already seen, so an idempotent no-op lets the entry through free
// while entry_fees_held still climbs. That gap between recorded and actual
// escrow is what strands settlement later.
func TestCompetitionResubmitAfterWithdrawChargesTheFeeAgain(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Revolving door", tale(100), tale(10))
	grantInitial(t, bob)

	storyID := enter(t, bob, id)
	call(t, "DELETE", "/v1/competitions/"+id+"/submissions/me", bob, nil).
		expect(http.StatusNoContent)
	call(t, "POST", "/v1/competitions/"+id+"/submissions/me", bob,
		map[string]any{"storyId": storyID}).expect(http.StatusNoContent)

	assertBalance(t, userAccount(bob), tale(initialGrantTale-10))
	assertBalance(t, escrowAccount(id), tale(110))

	// The recorded escrow must never exceed what the escrow account actually
	// holds, or settlement cannot pay out.
	held := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()["entryFeesHeld"].(string)
	prizePlusFees := new(big.Int)
	prizePlusFees.SetString(tale(100), 10)
	feesHeld := new(big.Int)
	feesHeld.SetString(held, 10)
	escrowHas := new(big.Int)
	escrowHas.SetString(dbBalance(t, escrowAccount(id)), 10)
	if new(big.Int).Add(prizePlusFees, feesHeld).Cmp(escrowHas) > 0 {
		t.Errorf("competition claims to hold prize %s + fees %s but escrow has only %s",
			tale(100), held, escrowHas)
	}
	assertLedgerIntact(t)
}

// ── voting ──────────────────────────────────────────────────────────────────

func TestCompetitionBallotRules(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Ballots", tale(100), "0")
	grantInitial(t, bob, carol, dave)
	enter(t, bob, id)
	enter(t, carol, id)
	registerVoter(t, dave, id)
	// Entrants joined when they entered; they still need standing to vote.
	agedProfile(t, bob)
	agedProfile(t, carol)

	ballot := "/v1/competitions/" + id + "/ballots/me"

	// Voting before the voting phase is refused.
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusUnprocessableEntity)

	advance(t, alice, id, "voting")

	// You cannot vote for yourself.
	call(t, "PUT", ballot, bob, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusUnprocessableEntity)
	// Nor for someone who did not submit.
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{dave}}).
		expect(http.StatusUnprocessableEntity)
	// Nor for more than three entries.
	call(t, "PUT", ballot, dave, map[string]any{
		"submissionIds": []string{bob, carol, "x", "y"}}).
		expect(http.StatusUnprocessableEntity)

	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob, carol}}).
		expect(http.StatusNoContent)

	got := get(t, ballot, dave).expect(http.StatusOK).json()
	if len(got["submissionIds"].([]any)) != 2 {
		t.Errorf("ballot = %v, want 2 choices", got["submissionIds"])
	}

	// Recasting replaces the previous choices rather than appending, and does
	// not count as a second voter.
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusNoContent)
	recast := get(t, ballot, dave).expect(http.StatusOK).json()
	if len(recast["submissionIds"].([]any)) != 1 {
		t.Errorf("recast ballot = %v, want 1 choice", recast["submissionIds"])
	}
	after := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if after["ballotCount"].(float64) != 1 {
		t.Errorf("ballotCount = %v, want 1 after a recast", after["ballotCount"])
	}

	// A voter who has not voted gets an empty ballot, not a 404.
	empty := get(t, ballot, carol).expect(http.StatusOK).json()
	if len(empty["submissionIds"].([]any)) != 0 {
		t.Errorf("expected an empty ballot, got %v", empty["submissionIds"])
	}
}

// A ballot decides a winner-take-all prize, and it used to require nothing
// but an authenticated uid — so a slate of throwaway accounts, free and
// unverified to create, could hand their owner any competition on the
// platform. Eligibility now costs something: registration that closes with
// entries, and a profile older than the contest.
func TestBallotRequiresParticipation(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Eligibility", tale(100), "0")
	grantInitial(t, bob, carol, dave)
	enter(t, bob, id)
	registerVoter(t, dave, id)
	// carol has standing but never joined; a stranger is exactly the account a
	// sybil slate is made of.
	agedProfile(t, carol)
	advance(t, alice, id, "voting")

	ballot := "/v1/competitions/" + id + "/ballots/me"
	call(t, "PUT", ballot, carol, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusForbidden)
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusNoContent)

	after := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if after["ballotCount"].(float64) != 1 {
		t.Errorf("ballotCount = %v, want only the eligible voter", after["ballotCount"])
	}
}

func TestBallotRequiresAnEstablishedProfile(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Standing", tale(100), "0")
	grantInitial(t, bob, carol, dave)
	enter(t, bob, id)
	// Both joined; neither has standing yet.
	call(t, "PUT", "/v1/competitions/"+id+"/join", carol, nil).expect(http.StatusNoContent)
	call(t, "PUT", "/v1/competitions/"+id+"/join", dave, nil).expect(http.StatusNoContent)
	call(t, "PUT", "/v1/profiles/me", dave,
		map[string]any{"username": "user_dave"}).expect(http.StatusCreated)
	advance(t, alice, id, "voting")

	ballot := "/v1/competitions/" + id + "/ballots/me"
	// No profile at all.
	call(t, "PUT", ballot, carol, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusForbidden)
	// A profile created minutes ago is what a sybil slate has; the age gate is
	// what it cannot fake.
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusForbidden)

	if _, err := testPool.Exec(context.Background(),
		`UPDATE public_profiles SET created_at=now()-interval '30 days' WHERE user_id=$1`,
		dave); err != nil {
		t.Fatal(err)
	}
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusNoContent)
}

// The per-competition ceiling lived in the schema and was ignored by a
// hard-coded 3.
func TestBallotHonorsMaxVotesPerUser(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "One vote each", tale(100), "0")
	grantInitial(t, bob, carol, dave)
	enter(t, bob, id)
	enter(t, carol, id)
	registerVoter(t, dave, id)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE competitions SET max_votes_per_user=1 WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	advance(t, alice, id, "voting")

	ballot := "/v1/competitions/" + id + "/ballots/me"
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob, carol}}).
		expect(http.StatusUnprocessableEntity)
	call(t, "PUT", ballot, dave, map[string]any{"submissionIds": []string{bob}}).
		expect(http.StatusNoContent)
}

// ── settlement ──────────────────────────────────────────────────────────────

func TestCompetitionSettlementPaysWinnerAndHost(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Settled", tale(100), tale(10))
	grantInitial(t, bob, carol, dave)
	enter(t, bob, id)
	enter(t, carol, id)
	registerVoter(t, dave, id)
	advance(t, alice, id, "voting")

	call(t, "PUT", "/v1/competitions/"+id+"/ballots/me", dave,
		map[string]any{"submissionIds": []string{bob}}).expect(http.StatusNoContent)

	settled := call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).
		expect(http.StatusOK).json()
	if settled["phase"] != "settled" {
		t.Fatalf("phase = %v, want settled", settled["phase"])
	}
	if settled["resultsDigest"] == "" || settled["resultsDigest"] == nil {
		t.Errorf("settlement must record a results digest")
	}
	results := settled["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("expected 2 ranked results, got %d", len(results))
	}
	top := results[0].(map[string]any)
	if top["userId"] != bob || top["rank"].(float64) != 1 || top["amount"] != tale(100) {
		t.Errorf("winner row = %v", top)
	}
	if runner := results[1].(map[string]any); runner["amount"] != "0" {
		t.Errorf("non-winner was paid: %v", runner)
	}

	// 20 TALE of entry fees, 10% platform fee: 2 to the treasury, 18 to the host.
	assertBalance(t, userAccount(bob), tale(initialGrantTale-10+100))
	assertBalance(t, userAccount(carol), tale(initialGrantTale-10))
	assertBalance(t, userAccount(alice), tale(initialGrantTale-100+18))
	assertBalance(t, platformAccount(), tale(2))
	assertBalance(t, escrowAccount(id), "0")

	after := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if after["entryFeesHeld"] != "0" {
		t.Errorf("entryFeesHeld after settlement = %v", after["entryFeesHeld"])
	}
	assertLedgerIntact(t)
}

func TestCompetitionSettlementIsIdempotent(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Twice settled", tale(100), tale(10))
	grantInitial(t, bob, dave)
	enter(t, bob, id)
	registerVoter(t, dave, id)
	advance(t, alice, id, "voting")
	call(t, "PUT", "/v1/competitions/"+id+"/ballots/me", dave,
		map[string]any{"submissionIds": []string{bob}}).expect(http.StatusNoContent)

	call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).expect(http.StatusOK)
	before := dbBalance(t, userAccount(bob))
	call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).expect(http.StatusOK)

	if now := dbBalance(t, userAccount(bob)); now != before {
		t.Errorf("second settlement paid the winner again: %s then %s", before, now)
	}
	assertBalance(t, escrowAccount(id), "0")
	assertLedgerIntact(t)
}

func TestCompetitionSettlementWithNoVotesReturnsThePrize(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Nobody voted", tale(100), "0")
	grantInitial(t, bob)
	enter(t, bob, id)
	advance(t, alice, id, "voting")

	call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).expect(http.StatusOK)

	// With no votes there is no winner, so the prize goes back to the host
	// rather than to the earliest submitter.
	assertBalance(t, userAccount(alice), tale(initialGrantTale))
	assertBalance(t, userAccount(bob), tale(initialGrantTale))
	assertBalance(t, escrowAccount(id), "0")
	assertLedgerIntact(t)
}

func TestCompetitionSettlementIsCreatorOrAdminOnly(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Not yours", tale(100), "0")
	grantInitial(t, bob)
	enter(t, bob, id)
	advance(t, alice, id, "voting")

	call(t, "POST", "/v1/competitions/"+id+"/settle", bob, nil).expect(http.StatusForbidden)
	// X-Admin is honoured in dev auth mode.
	call(t, "POST", "/v1/competitions/"+id+"/settle", bob, nil,
		map[string]string{"X-Admin": "true"}).expect(http.StatusOK)
	assertLedgerIntact(t)
}

func TestCompetitionSettlementRefusedBeforeVoting(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Too early", tale(100), "0")

	call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).
		expect(http.StatusUnprocessableEntity)
	assertBalance(t, escrowAccount(id), tale(100))
}

// Settlement builds one ledger transfer, and ledger_postings is keyed
// (transfer_key, account_id). The two scenarios below are the ones where the
// same account is credited twice in that transfer — the returned prize plus
// the host's cut of the entry fees. Before the postings were merged, both
// aborted the transfer on a duplicate-key violation, after the phase had
// already been committed as "settling" on a separate connection: settle then
// re-entered the same failure forever and cancel refused the phase outright,
// so the escrow was unreachable by any code path.
func TestCompetitionSettlementWithFeesAndNoWinnerPaysTheHostOnce(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Fees, nobody voted", tale(100), tale(10))
	grantInitial(t, bob, carol)
	enter(t, bob, id)
	enter(t, carol, id)
	advance(t, alice, id, "voting")

	settled := call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).
		expect(http.StatusOK).json()
	if settled["phase"] != "settled" {
		t.Fatalf("phase = %v, want settled", settled["phase"])
	}

	// No winner, so the 100 prize returns to alice; 20 in fees splits 2 to the
	// treasury and 18 to alice as host. Both credits land on one account and
	// must post as a single net line of 118.
	assertBalance(t, userAccount(alice), tale(initialGrantTale-100+100+18))
	assertBalance(t, userAccount(bob), tale(initialGrantTale-10))
	assertBalance(t, userAccount(carol), tale(initialGrantTale-10))
	assertBalance(t, platformAccount(), tale(2))
	assertBalance(t, escrowAccount(id), "0")
	assertLedgerIntact(t)
}

func TestCompetitionSettlementWhenTheHostsOwnEntryWins(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Host wins", tale(100), tale(10))
	grantInitial(t, bob, dave)
	enter(t, alice, id)
	enter(t, bob, id)
	registerVoter(t, dave, id)
	advance(t, alice, id, "voting")
	call(t, "PUT", "/v1/competitions/"+id+"/ballots/me", dave,
		map[string]any{"submissionIds": []string{alice}}).expect(http.StatusNoContent)

	call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).expect(http.StatusOK)

	// alice escrowed 100 and paid a 10 entry fee, then took the 100 prize and
	// 18 of the 20 in fees.
	assertBalance(t, userAccount(alice), tale(initialGrantTale-100-10+100+18))
	assertBalance(t, userAccount(bob), tale(initialGrantTale-10))
	assertBalance(t, platformAccount(), tale(2))
	assertBalance(t, escrowAccount(id), "0")
	assertLedgerIntact(t)
}

// A settlement that cannot pay out must leave nothing behind. The phase claim
// used to commit on its own connection, so a failure here stranded the
// competition in "settling"; now the claim, the payout and the results share
// one transaction and roll back together.
func TestFailedSettlementLeavesTheCompetitionRetryable(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Payout fails", tale(100), "0")
	grantInitial(t, bob, dave)
	enter(t, bob, id)
	registerVoter(t, dave, id)
	advance(t, alice, id, "voting")
	call(t, "PUT", "/v1/competitions/"+id+"/ballots/me", dave,
		map[string]any{"submissionIds": []string{bob}}).expect(http.StatusNoContent)

	// Empty the escrow behind the API's back so the payout hits the
	// insufficient-funds guard inside the transfer.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE token_accounts SET balance=0 WHERE account_id=$1`, escrowAccount(id)); err != nil {
		t.Fatal(err)
	}
	call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).
		expect(http.StatusUnprocessableEntity)

	after := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if after["phase"] != "voting" {
		t.Fatalf("a failed settlement left phase %v, want voting", after["phase"])
	}
	var transfers int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM ledger_transfers WHERE idempotency_key=$1`,
		"escrow:release:"+id).Scan(&transfers); err != nil {
		t.Fatal(err)
	}
	if transfers != 0 {
		t.Errorf("a failed settlement recorded %d payout transfers", transfers)
	}

	// Put the escrow back and the same call now succeeds.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE token_accounts SET balance=$2::numeric WHERE account_id=$1`,
		escrowAccount(id), tale(100)); err != nil {
		t.Fatal(err)
	}
	settled := call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).
		expect(http.StatusOK).json()
	if settled["phase"] != "settled" {
		t.Errorf("retried settlement left phase %v", settled["phase"])
	}
	assertBalance(t, escrowAccount(id), "0")
}

// Rows the old code already stranded in "settling" are reset to "voting" by
// migration 000014 when no payout committed. Settle must also finish one that
// arrives in that phase directly, which is what the migration relies on for
// the rows it deliberately leaves alone.
func TestSettlementResumesFromTheSettlingPhase(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Stranded", tale(100), tale(10))
	grantInitial(t, bob)
	enter(t, bob, id)
	advance(t, alice, id, "voting")
	if _, err := testPool.Exec(context.Background(),
		`UPDATE competitions SET phase='settling' WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}

	settled := call(t, "POST", "/v1/competitions/"+id+"/settle", alice, nil).
		expect(http.StatusOK).json()
	if settled["phase"] != "settled" {
		t.Fatalf("phase = %v, want settled", settled["phase"])
	}
	assertBalance(t, escrowAccount(id), "0")
	assertLedgerIntact(t)
}

// ── cancellation ────────────────────────────────────────────────────────────

func TestCompetitionCancelRefundsPrizeAndEveryEntryFee(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Called off", tale(100), tale(10))
	grantInitial(t, bob, carol)
	enter(t, bob, id)
	enter(t, carol, id)

	cancelled := call(t, "POST", "/v1/competitions/"+id+"/cancel", alice,
		map[string]any{"reason": "not enough entries"}).expect(http.StatusOK).json()
	if cancelled["phase"] != "cancelled" {
		t.Fatalf("phase = %v, want cancelled", cancelled["phase"])
	}

	assertBalance(t, userAccount(alice), tale(initialGrantTale))
	assertBalance(t, userAccount(bob), tale(initialGrantTale))
	assertBalance(t, userAccount(carol), tale(initialGrantTale))
	assertBalance(t, escrowAccount(id), "0")
	assertBalance(t, platformAccount(), "0")
	assertLedgerIntact(t)
}

func TestCompetitionCancelIsIdempotentAndBlockedAfterSettlement(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Cancel twice", tale(100), tale(10))
	grantInitial(t, bob)
	enter(t, bob, id)

	call(t, "POST", "/v1/competitions/"+id+"/cancel", alice,
		map[string]any{"reason": "first"}).expect(http.StatusOK)
	before := dbBalance(t, userAccount(bob))
	call(t, "POST", "/v1/competitions/"+id+"/cancel", alice,
		map[string]any{"reason": "second"}).expect(http.StatusOK)
	if now := dbBalance(t, userAccount(bob)); now != before {
		t.Errorf("second cancel refunded again: %s then %s", before, now)
	}
	assertLedgerIntact(t)

	// A settled competition can no longer be cancelled.
	other := openCompetition(t, alice, "Already settled", tale(50), "0")
	grantInitial(t, carol, dave)
	enter(t, carol, other)
	registerVoter(t, dave, other)
	advance(t, alice, other, "voting")
	call(t, "PUT", "/v1/competitions/"+other+"/ballots/me", dave,
		map[string]any{"submissionIds": []string{carol}}).expect(http.StatusNoContent)
	call(t, "POST", "/v1/competitions/"+other+"/settle", alice, nil).expect(http.StatusOK)
	call(t, "POST", "/v1/competitions/"+other+"/cancel", alice,
		map[string]any{"reason": "too late"}).expect(http.StatusUnprocessableEntity)
	assertLedgerIntact(t)
}

func TestCompetitionCancelIsCreatorOrAdminOnly(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Guarded", tale(100), "0")

	call(t, "POST", "/v1/competitions/"+id+"/cancel", bob,
		map[string]any{"reason": "mine now"}).expect(http.StatusForbidden)
	assertBalance(t, escrowAccount(id), tale(100))
}

// ── phase machine and routing ───────────────────────────────────────────────

func TestCompetitionAdvanceRejectsIllegalTransitions(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Phases", tale(100), "0")

	// open -> settled is not a legal hop; settlement has its own endpoint.
	call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{"targetPhase": "settled"}).expect(http.StatusUnprocessableEntity)
	// Nor backwards.
	call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{"targetPhase": "draft"}).expect(http.StatusUnprocessableEntity)

	advance(t, alice, id, "voting")
	call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{"targetPhase": "open"}).expect(http.StatusUnprocessableEntity)

	call(t, "POST", "/v1/competitions/"+id+"/advance", bob,
		map[string]any{"targetPhase": "cancelled"}).expect(http.StatusForbidden)
}

// /advance used to apply any phase the caller named, which gave every
// money-moving transition a second door with no money logic behind it. A
// draft could be opened without escrowing the prize and an open competition
// "cancelled" without refunding the entry fees it had collected.
func TestAdvanceCannotOpenAnUnfundedDraft(t *testing.T) {
	reset(t)
	grantInitial(t, alice)
	draft := newDraft(t, alice, "Never funded", tale(500), tale(10))
	id := draft["id"].(string)

	call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{"targetPhase": "open"}).expect(http.StatusUnprocessableEntity)

	still := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if still["phase"] != "draft" {
		t.Fatalf("phase = %v, want draft", still["phase"])
	}
	// The prize was never escrowed, which is precisely why the competition
	// must not be reachable by entrants.
	assertBalance(t, userAccount(alice), tale(initialGrantTale))
	assertBalance(t, escrowAccount(id), "0")

	// Publishing is the door that funds it.
	call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": id}).expect(http.StatusOK)
	assertBalance(t, escrowAccount(id), tale(500))
	assertLedgerIntact(t)
}

func TestAdvanceCannotCancel(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "No silent cancel", tale(100), tale(10))
	grantInitial(t, bob)
	enter(t, bob, id)

	call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{"targetPhase": "cancelled"}).expect(http.StatusUnprocessableEntity)

	open := get(t, "/v1/competitions/"+id, alice).expect(http.StatusOK).json()
	if open["phase"] != "open" {
		t.Fatalf("phase = %v, want open", open["phase"])
	}
	assertBalance(t, escrowAccount(id), tale(110))
	assertBalance(t, userAccount(bob), tale(initialGrantTale-10))

	// The real cancel endpoint refunds, which is the whole reason /advance
	// must not be able to reach the phase.
	call(t, "POST", "/v1/competitions/"+id+"/cancel", alice,
		map[string]any{"reason": "changed my mind"}).expect(http.StatusOK)
	assertBalance(t, userAccount(bob), tale(initialGrantTale))
	assertBalance(t, userAccount(alice), tale(initialGrantTale))
	assertBalance(t, escrowAccount(id), "0")
	assertLedgerIntact(t)
}

// Only the clock decides. A target is accepted as an assertion about the
// transition already due, and rejected otherwise.
func TestAdvanceOnlyFollowsTheClock(t *testing.T) {
	reset(t)
	grantInitial(t, alice)
	now := time.Now().UTC()
	draft := call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
		"title": "Opens later", "description": "d", "category": "flash-fiction",
		"tags":        []string{"x"},
		"startDate":   now.Add(time.Hour).Format(time.RFC3339),
		"deadline":    now.Add(2 * time.Hour).Format(time.RFC3339),
		"prizeAmount": tale(10), "creatorName": "Alice",
		"votingDeadline": now.Add(3 * time.Hour).Format(time.RFC3339),
	}).expect(http.StatusCreated).json()
	id := draft["id"].(string)
	published := call(t, "POST", "/v1/competition-publish", alice,
		map[string]any{"competitionId": id}).expect(http.StatusOK).json()
	if published["phase"] != "scheduled" {
		t.Fatalf("a future start should publish as scheduled, got %v", published["phase"])
	}

	// Nothing is due yet: an empty target is a no-op, a named one is refused.
	idle := call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{}).expect(http.StatusOK).json()
	if idle["phase"] != "scheduled" {
		t.Errorf("advance moved a competition whose start has not arrived: %v", idle["phase"])
	}
	call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{"targetPhase": "open"}).expect(http.StatusUnprocessableEntity)

	// Bringing the start forward is a host action with its own endpoint; once
	// it is in the past the same advance call performs the transition.
	call(t, "PATCH", "/v1/competitions/"+id, alice, map[string]any{
		"startDate": now.Add(-time.Minute).Format(time.RFC3339),
	}).expect(http.StatusOK)
	opened := call(t, "POST", "/v1/competitions/"+id+"/advance", alice,
		map[string]any{"targetPhase": "open"}).expect(http.StatusOK).json()
	if opened["phase"] != "open" {
		t.Fatalf("phase = %v, want open", opened["phase"])
	}
}

// Defence in depth: even if a competition were opened without its prize in
// escrow, it must not be able to take an entry fee it could never pay out
// against.
func TestSubmitRejectsUnfundedCompetition(t *testing.T) {
	reset(t)
	id := openCompetition(t, alice, "Escrow drained", tale(100), tale(10))
	grantInitial(t, bob)
	// Empty the escrow behind the API's back to stand in for a competition
	// that reached "open" without being funded.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE token_accounts SET balance=0 WHERE account_id=$1`, escrowAccount(id)); err != nil {
		t.Fatal(err)
	}

	story := newStory(t, bob, "bob's entry")
	call(t, "PUT", "/v1/competitions/"+id+"/join", bob, nil).expect(http.StatusNoContent)
	call(t, "POST", "/v1/competitions/"+id+"/submissions/me", bob,
		map[string]any{"storyId": story["id"]}).expect(http.StatusUnprocessableEntity)

	// The rejection must be complete: no fee taken, no submission recorded.
	assertBalance(t, userAccount(bob), tale(initialGrantTale))
	if subs := get(t, "/v1/competitions/"+id+"/submissions", bob).
		expect(http.StatusOK).list(); len(subs) != 0 {
		t.Errorf("a rejected submission was recorded: %d entries", len(subs))
	}
	// Undo the injected corruption before the invariant check, which would
	// otherwise flag the escrow this test emptied by hand.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE token_accounts SET balance=$2::numeric WHERE account_id=$1`,
		escrowAccount(id), tale(100)); err != nil {
		t.Fatal(err)
	}
	assertLedgerIntact(t)
}

func TestCompetitionDiscardDraft(t *testing.T) {
	reset(t)
	grantInitial(t, alice)
	draft := newDraft(t, alice, "Throwaway", tale(10), "0")
	id := draft["id"].(string)

	// Someone else cannot discard it.
	call(t, "DELETE", "/v1/competitions/"+id, bob, nil).expect(http.StatusForbidden)

	call(t, "DELETE", "/v1/competitions/"+id, alice, nil).expect(http.StatusNoContent)
	get(t, "/v1/competitions/"+id, alice).expect(http.StatusNotFound)

	// A published competition is not a draft and cannot be discarded.
	live := openCompetition(t, alice, "Live", tale(10), "0")
	call(t, "DELETE", "/v1/competitions/"+live, alice, nil).expect(http.StatusForbidden)
}

func TestCompetitionUnknownAndMalformedIDs(t *testing.T) {
	reset(t)

	// A malformed id must 404 rather than blowing up the uuid cast with a 500.
	get(t, "/v1/competitions/not-a-uuid", alice).expect(http.StatusNotFound)
	get(t, "/v1/competitions/11111111-1111-1111-1111-111111111111", alice).
		expect(http.StatusNotFound)
	call(t, "PUT", "/v1/competitions/not-a-uuid/join", alice, nil).expect(http.StatusNotFound)
	call(t, "POST", "/v1/competitions/11111111-1111-1111-1111-111111111111/settle",
		alice, nil).expect(http.StatusNotFound)

	// Empty collections serialize as [], never null.
	get(t, "/v1/competitions", alice).expect(http.StatusOK).list()
	get(t, "/v1/me/competitions/drafts", alice).expect(http.StatusOK).list()
	get(t, "/v1/competitions/11111111-1111-1111-1111-111111111111/submissions", alice).
		expect(http.StatusOK).list()
}
