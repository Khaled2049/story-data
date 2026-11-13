package e2e

// The production service path through the real router: AUTH_MODE=production,
// a backend proving itself with a Google ID token. Google's key check is
// replaced by a fake that trusts a token whose signature segment is "signed";
// everything after it — allowlist, owner-only, bans — is the real code.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kh1011/novelsync-story-data/internal/auth"
	"github.com/kh1011/novelsync-story-data/internal/httpapi"
	"github.com/kh1011/novelsync-story-data/internal/store"
	"google.golang.org/api/idtoken"
)

const (
	prodAudience = "https://story-data.test.run.app"
	prodAgents   = "agents-run@test.iam.gserviceaccount.com"
)

func prodServer(t *testing.T) *httptest.Server {
	t.Helper()
	validate := func(_ context.Context, tok, aud string) (*idtoken.Payload, error) {
		parts := strings.Split(tok, ".")
		if sig, _ := base64.RawURLEncoding.DecodeString(parts[len(parts)-1]); string(sig) != "signed" {
			return nil, errors.New("bad signature")
		}
		p, err := idtoken.ParsePayload(tok)
		if err == nil && p.Audience != aud {
			err = errors.New("audience mismatch")
		}
		return p, err
	}
	v := auth.New("production", "test-project", "").
		WithServiceAccounts([]string{prodAudience}, []string{prodAgents}, validate)
	s := httptest.NewServer(httpapi.New(store.New(testPool), v, nil, httpapi.RateLimit{}))
	t.Cleanup(s.Close)
	return s
}

func serviceIDToken(t *testing.T, email string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"iss": "https://accounts.google.com", "aud": prodAudience, "exp": 4102444800,
		"email": email, "email_verified": true,
	})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(raw) + "." + enc([]byte("signed"))
}

// asService calls srv as the agents service asserting uid.
func asService(t *testing.T, srv *httptest.Server, method, path, uid string, headers ...map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(auth.ServiceIDTokenHeader, "Bearer "+serviceIDToken(t, prodAgents))
	req.Header.Set("X-User-ID", uid)
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func publishDirectly(t *testing.T, storyID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE stories SET is_published=true WHERE id=$1`, storyID); err != nil {
		t.Fatal(err)
	}
}

func TestProductionServiceReadsOnlyTheAssertedUsersStories(t *testing.T) {
	reset(t)
	srv := prodServer(t)
	story := newStory(t, alice, "Salt and Sextant")["id"].(string)
	publishDirectly(t, story)

	if got := asService(t, srv, "GET", "/v1/stories/"+story, alice); got != http.StatusOK {
		t.Fatalf("owner read through the service = %d, want 200", got)
	}
	// Published, so any signed-in reader may open it in the browser. A
	// service asserting bob must not: that is how drafts and private
	// worldbuilding would leak into another user's assistant.
	for _, path := range []string{
		"/v1/stories/" + story,
		"/v1/stories/" + story + "/chapters",
		"/v1/stories/" + story + "/characters",
	} {
		if got := asService(t, srv, "GET", path, bob); got != http.StatusNotFound {
			t.Errorf("GET %s asserting a non-owner = %d, want 404", path, got)
		}
	}
	if got := asService(t, srv, "GET", "/v1/stories/not-a-uuid", alice); got != http.StatusNotFound {
		t.Errorf("malformed story id = %d, want 404", got)
	}
}

func TestProductionServiceIsHeldToItsRoutes(t *testing.T) {
	reset(t)
	srv := prodServer(t)
	story := newStory(t, alice, "Salt and Sextant")["id"].(string)

	refused := []struct{ method, path string }{
		{"DELETE", "/v1/stories/" + story},
		{"POST", "/v1/me/writer-agreement"},
		{"GET", "/v1/me/reading-history"},
		{"POST", "/v1/admin/token-grants"},
		{"GET", "/v1/public/stories"},
	}
	for _, r := range refused {
		if got := asService(t, srv, r.method, r.path, alice, map[string]string{"X-Admin": "true"}); got != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", r.method, r.path, got)
		}
	}
	// The refused delete did not reach the store.
	if got := asService(t, srv, "GET", "/v1/stories/"+story, alice); got != http.StatusOK {
		t.Fatalf("story after refused delete = %d, want 200", got)
	}
}

func TestProductionRefusesOtherServiceCredentials(t *testing.T) {
	reset(t)
	srv := prodServer(t)
	newStory(t, alice, "Salt and Sextant")

	send := func(headers map[string]string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/v1/stories", nil)
		req.Header.Set("X-User-ID", alice)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	cases := map[string]map[string]string{
		"the retired shared secret": {auth.ServiceTokenHeader: testServiceToken},
		"another service account": {auth.ServiceIDTokenHeader: "Bearer " +
			serviceIDToken(t, "intruder@test.iam.gserviceaccount.com")},
		"a forged signature": {auth.ServiceIDTokenHeader: "Bearer " +
			strings.TrimSuffix(serviceIDToken(t, prodAgents), base64.RawURLEncoding.EncodeToString([]byte("signed"))) + "Zm9yZ2Vk"},
		"X-User-ID alone": {},
	}
	for name, headers := range cases {
		if got := send(headers); got != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, got)
		}
	}
}

func TestProductionServiceHonoursBans(t *testing.T) {
	reset(t)
	srv := prodServer(t)
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO user_bans (user_id, banned_by, reason) VALUES ($1, 'admin', 'test')`, alice); err != nil {
		t.Fatal(err)
	}
	if got := asService(t, srv, "GET", "/v1/stories", alice); got != http.StatusForbidden {
		t.Errorf("service call for a banned user = %d, want 403", got)
	}
}
