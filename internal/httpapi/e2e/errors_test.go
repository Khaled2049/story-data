package e2e

// How the service answers input it cannot use.
//
// A malformed request that reaches PostgreSQL and comes back as a constraint
// violation used to surface as 500. That is two problems: a client cannot tell
// "you sent something invalid" from "the server is broken", and because the
// 500s are attacker-triggerable at will, the error rate — the natural alert
// for a real outage — is trivially poisoned.

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestInvalidInputIsNot500(t *testing.T) {
	reset(t)
	grantInitial(t, alice)

	// A cursor that is valid base64 and valid JSON but carries an id the uuid
	// comparison cannot parse. It has to be built by hand: no encoder here
	// would produce it.
	badCursor := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"createdAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","id":"not-a-uuid"}`))

	cases := []struct {
		name, method, path string
		body               any
	}{
		{"title past the column ceiling", "POST", "/v1/stories",
			map[string]any{"title": strings.Repeat("A", 601), "description": "d", "authorName": "a", "tags": []string{"x"}}},
		{"competitionId that is not a uuid", "POST", "/v1/competition-drafts?competitionId=not-a-uuid",
			map[string]any{"title": "x", "description": "d", "category": "c", "tags": []string{}, "creatorName": "Alice"}},
		{"cursor carrying a non-uuid id", "GET", publicWallOf(alice) + "?cursor=" + badCursor, nil},
		{"rating outside its range", "POST", "/v1/stories/" + newPublishedStory(t, alice, "Rated")["id"].(string) + "/ratings",
			map[string]any{"rating": 99}},
	}
	for _, c := range cases {
		got := call(t, c.method, c.path, alice, c.body)
		if got.Status >= 500 {
			t.Errorf("%s = %d, want a 4xx — body: %s", c.name, got.Status, got.Body)
		}
		if got.Status < 400 {
			t.Errorf("%s = %d, want a rejection", c.name, got.Status)
		}
		// Whatever the status, the body stays generic: database detail
		// belongs in the log, not the response.
		if body := string(got.Body); strings.Contains(body, "SQLSTATE") || strings.Contains(body, "constraint") {
			t.Errorf("%s leaked database detail: %s", c.name, body)
		}
	}

	// Omitting tags used to hit a NOT NULL column and return 500. It is not
	// invalid input though — an absent optional list means an empty one — so
	// it succeeds rather than turning into a 422.
	created := call(t, "POST", "/v1/competition-drafts", alice, map[string]any{
		"title": "No tags", "description": "d", "category": "c", "creatorName": "Alice",
	}).expect(http.StatusCreated).json()
	if tags, ok := created["tags"].([]any); !ok || len(tags) != 0 {
		t.Errorf("tags = %v, want an empty array", created["tags"])
	}
}

// Every response carries an id an operator can grep the logs for.
func TestResponsesCarryARequestID(t *testing.T) {
	reset(t)

	fresh := get(t, "/v1/public/stories", "").expect(http.StatusOK)
	if fresh.Header.Get("X-Request-ID") == "" {
		t.Error("no X-Request-ID on a response")
	}

	// A caller's own id is kept, so a trace survives across services.
	tagged := get(t, "/v1/public/stories", "", map[string]string{"X-Request-ID": "trace-abc"})
	if got := tagged.Header.Get("X-Request-ID"); got != "trace-abc" {
		t.Errorf("X-Request-ID = %q, want the inbound trace-abc", got)
	}

	// Rejections are logged and correlated too — during an attack those are
	// the interesting requests.
	rejected := get(t, "/v1/stories", "").expect(http.StatusUnauthorized)
	if rejected.Header.Get("X-Request-ID") == "" {
		t.Error("no X-Request-ID on a rejected request")
	}
}
