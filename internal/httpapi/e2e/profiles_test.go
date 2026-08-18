package e2e

// Public profiles and the follow graph.
//
// A profile is the one record a user writes about themselves that everyone
// else can read, so the interesting assertions are about what the server
// refuses to store (usernames, wallet addresses) and what a PATCH does to the
// fields it was not given.

import (
	"net/http"
	"strings"
	"testing"
)

func profileOf(uid string) string { return "/v1/public/profiles/" + uid }

func putProfile(t *testing.T, uid string, body map[string]any) response {
	t.Helper()
	return call(t, "PUT", "/v1/profiles/me", uid, body)
}

// ── create and read ─────────────────────────────────────────────────────────

func TestProfileCreateAndRead(t *testing.T) {
	reset(t)

	created := putProfile(t, alice, map[string]any{
		"username": "alice_v", "bio": "Writes at night.", "occupation": "Cartographer",
		"location": "Lisbon", "photoUrl": "https://example.test/a.png",
	}).expect(http.StatusCreated).json()

	if created["username"] != "alice_v" || created["userId"] != alice {
		t.Errorf("profile = %v", created)
	}
	// A policy the caller did not set defaults to the open wall, so accounts
	// that predate the setting keep the behaviour they had.
	if created["guestbookPolicy"] != "everyone" {
		t.Errorf("guestbookPolicy = %v, want everyone", created["guestbookPolicy"])
	}

	// Public read, no credentials.
	public := get(t, profileOf(alice), "").expect(http.StatusOK).json()
	if public["bio"] != "Writes at night." {
		t.Errorf("bio = %v", public["bio"])
	}

	mine := get(t, "/v1/profiles/me", alice).expect(http.StatusOK).json()
	if mine["userId"] != alice {
		t.Errorf("/profiles/me = %v", mine)
	}

	get(t, profileOf("user-nobody"), "").expect(http.StatusNotFound)
	get(t, "/v1/profiles/me", "").expect(http.StatusUnauthorized)
	get(t, "/v1/profiles/me", bob).expect(http.StatusNotFound)
}

func TestProfileUsernameRules(t *testing.T) {
	reset(t)

	// 3–20 characters, letters, digits and underscore only.
	for _, bad := range []string{"ab", strings.Repeat("a", 21), "has space", "has-dash", "héllo", ""} {
		putProfile(t, alice, map[string]any{"username": bad}).
			expect(http.StatusUnprocessableEntity)
	}
	// Username is the one required field.
	putProfile(t, alice, map[string]any{"bio": "no name"}).
		expect(http.StatusUnprocessableEntity)

	putProfile(t, alice, map[string]any{"username": "alice_v"}).
		expect(http.StatusCreated)

	// Uniqueness is case-insensitive and reported as a conflict, not a
	// validation failure — the input was well-formed, it is just taken.
	putProfile(t, bob, map[string]any{"username": "ALICE_V"}).
		expect(http.StatusConflict)
	putProfile(t, bob, map[string]any{"username": "bob_v"}).
		expect(http.StatusCreated)

	// Re-claiming your own username is not a conflict with yourself.
	putProfile(t, alice, map[string]any{"username": "alice_v", "bio": "edited"}).
		expect(http.StatusCreated)
}

func TestProfileWalletAddressRules(t *testing.T) {
	reset(t)
	valid := "0x" + strings.Repeat("a", 40)

	created := putProfile(t, alice, map[string]any{
		"username": "alice_v", "walletAddress": strings.ToUpper(valid),
	}).expect(http.StatusCreated).json()
	// Stored lowercase so a checksummed address and a plain one are the same
	// account.
	if created["walletAddress"] != valid {
		t.Errorf("walletAddress = %v, want %s", created["walletAddress"], valid)
	}

	for _, bad := range []string{"0x123", valid + "0", "0y" + strings.Repeat("a", 40),
		"0x" + strings.Repeat("z", 40)} {
		putProfile(t, alice, map[string]any{"username": "alice_v", "walletAddress": bad}).
			expect(http.StatusUnprocessableEntity)
	}

	// An empty wallet is allowed and clears the field.
	cleared := putProfile(t, alice, map[string]any{"username": "alice_v", "walletAddress": ""}).
		expect(http.StatusCreated).json()
	if v, ok := cleared["walletAddress"]; ok && v != "" {
		t.Errorf("wallet was not cleared: %v", v)
	}
}

// ── patch semantics ─────────────────────────────────────────────────────────

func TestProfilePatchLeavesOmittedFieldsAlone(t *testing.T) {
	reset(t)
	putProfile(t, alice, map[string]any{
		"username": "alice_v", "bio": "Original bio.", "occupation": "Cartographer",
	}).expect(http.StatusCreated)

	// A PATCH names only what it changes; PUT is the full replacement.
	patched := call(t, "PATCH", "/v1/profiles/me", alice,
		map[string]any{"bio": "Revised bio."}).expect(http.StatusOK).json()
	if patched["bio"] != "Revised bio." {
		t.Errorf("bio = %v", patched["bio"])
	}
	if patched["occupation"] != "Cartographer" {
		t.Errorf("a patch cleared an omitted field: %v", patched["occupation"])
	}
	if patched["username"] != "alice_v" {
		t.Errorf("username = %v", patched["username"])
	}

	// An explicit empty string does clear — that is the distinction the
	// pointer-valued input exists to make.
	cleared := call(t, "PATCH", "/v1/profiles/me", alice,
		map[string]any{"bio": ""}).expect(http.StatusOK).json()
	if v, ok := cleared["bio"]; ok && v != "" {
		t.Errorf("bio was not cleared: %v", v)
	}

	// A PUT that omits a field does clear it.
	replaced := putProfile(t, alice, map[string]any{"username": "alice_v"}).
		expect(http.StatusCreated).json()
	if v, ok := replaced["occupation"]; ok && v != "" {
		t.Errorf("PUT should replace, leaving occupation empty, got %v", v)
	}

	// Patching a profile that does not exist yet is a 404 — create with PUT.
	call(t, "PATCH", "/v1/profiles/me", bob, map[string]any{"bio": "x"}).
		expect(http.StatusNotFound)
}

func TestProfileFieldLimitsAndPolicy(t *testing.T) {
	reset(t)

	putProfile(t, alice, map[string]any{
		"username": "alice_v", "bio": strings.Repeat("x", 301),
	}).expect(http.StatusUnprocessableEntity)
	putProfile(t, alice, map[string]any{
		"username": "alice_v", "occupation": strings.Repeat("x", 51),
	}).expect(http.StatusUnprocessableEntity)
	putProfile(t, alice, map[string]any{
		"username": "alice_v", "location": strings.Repeat("x", 51),
	}).expect(http.StatusUnprocessableEntity)

	for _, policy := range []string{"everyone", "followers", "following", "mutuals", "nobody"} {
		putProfile(t, alice, map[string]any{"username": "alice_v", "guestbookPolicy": policy}).
			expect(http.StatusCreated)
	}
	putProfile(t, alice, map[string]any{"username": "alice_v", "guestbookPolicy": "friends"}).
		expect(http.StatusUnprocessableEntity)

	putProfile(t, "", map[string]any{"username": "anon_v"}).expect(http.StatusUnauthorized)
}

// ── directory ───────────────────────────────────────────────────────────────

func TestProfileDirectoryBatchAndSearch(t *testing.T) {
	reset(t)
	newProfile(t, alice, "alice_v", "everyone")
	newProfile(t, bob, "bobby_v", "everyone")
	newProfile(t, carol, "carla_v", "everyone")

	// The batch form is what list surfaces use instead of one request per row.
	batch := get(t, "/v1/public/profiles?ids="+alice+","+carol, "").
		expect(http.StatusOK).list()
	if len(batch) != 2 {
		t.Fatalf("batch returned %d profiles, want 2", len(batch))
	}
	// Returned in the order asked for, so a caller can zip it against its rows.
	if batch[0]["userId"] != alice || batch[1]["userId"] != carol {
		t.Errorf("batch order = %v", batch)
	}
	// Unknown ids are skipped rather than erroring or padding with nulls.
	mixed := get(t, "/v1/public/profiles?ids="+alice+",user-nobody", "").
		expect(http.StatusOK).list()
	if len(mixed) != 1 {
		t.Errorf("expected unknown ids to be skipped, got %v", mixed)
	}

	// Prefix search is case-insensitive on the username.
	found := get(t, "/v1/public/profiles?query=BOB", "").expect(http.StatusOK).list()
	if len(found) != 1 || found[0]["username"] != "bobby_v" {
		t.Errorf("search = %v", found)
	}
	// An exact username is a prefix of itself.
	if exact := get(t, "/v1/public/profiles?query=bobby_v", "").expect(http.StatusOK).list(); len(exact) != 1 {
		t.Errorf("exact-match search = %v", exact)
	}
	if none := get(t, "/v1/public/profiles?query=zzz", "").expect(http.StatusOK).list(); len(none) != 0 {
		t.Errorf("expected no matches, got %v", none)
	}

	// Underscore is a legal username character and a LIKE wildcard, so it has
	// to match literally rather than standing in for any character.
	newProfile(t, dave, "ada_b", "everyone")
	newProfile(t, erin, "adaXb", "everyone")
	underscore := get(t, "/v1/public/profiles?query=ada_b", "").expect(http.StatusOK).list()
	if len(underscore) != 1 || underscore[0]["username"] != "ada_b" {
		t.Errorf("underscore matched as a wildcard: %v", underscore)
	}
	// A bare wildcard is likewise literal, and matches nothing.
	if pct := get(t, "/v1/public/profiles?query=%25", "").expect(http.StatusOK).list(); len(pct) != 0 {
		t.Errorf("a percent sign matched as a wildcard: %v", pct)
	}

	// With neither ids nor a query it is a plain directory listing — every
	// profile created above, newest first.
	if all := get(t, "/v1/public/profiles", "").expect(http.StatusOK).list(); len(all) != 5 {
		t.Errorf("directory listing returned %d profiles, want 5", len(all))
	}

	// The two selectors are mutually exclusive.
	get(t, "/v1/public/profiles?query=bob&ids="+alice, "").expect(http.StatusBadRequest)
	get(t, "/v1/public/profiles?limit=0", "").expect(http.StatusBadRequest)
	get(t, "/v1/public/profiles?limit=51", "").expect(http.StatusBadRequest)
}

// ── follows ─────────────────────────────────────────────────────────────────

func TestProfileFollowGraphShape(t *testing.T) {
	reset(t)

	call(t, "PUT", "/v1/profiles/"+bob+"/follow", alice, nil).expect(http.StatusNoContent)
	call(t, "PUT", "/v1/profiles/"+carol+"/follow", alice, nil).expect(http.StatusNoContent)

	mine := get(t, "/v1/me/follows", alice).expect(http.StatusOK).json()
	following := mine["following"].([]any)
	if len(following) != 2 {
		t.Fatalf("alice follows %v, want 2", following)
	}
	// Both directions are returned so a caller can answer "do they follow me?"
	// without a second request — the guestbook policy needs both.
	if len(mine["followers"].([]any)) != 0 {
		t.Errorf("alice should have no followers: %v", mine["followers"])
	}

	// Following someone who has no profile is allowed — ids come from Firebase
	// and a user may be followed before they ever write a profile.
	call(t, "PUT", "/v1/profiles/user-ghost/follow", alice, nil).expect(http.StatusNoContent)

	call(t, "PUT", "/v1/profiles/"+alice+"/follow", alice, nil).
		expect(http.StatusUnprocessableEntity)
	call(t, "PUT", "/v1/profiles/"+bob+"/follow", "", nil).expect(http.StatusUnauthorized)
}
