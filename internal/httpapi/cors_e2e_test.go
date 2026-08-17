package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kh1011/novelsync-story-data/internal/auth"
	"github.com/kh1011/novelsync-story-data/internal/httpapi"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

const allowedOrigin = "https://thetaletribe.com"

func corsServer(t *testing.T, origins ...string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(httpapi.New(store.New(testPool), auth.New("dev", "", testServiceToken), origins))
	t.Cleanup(s.Close)
	return s
}

func preflight(t *testing.T, srv *httptest.Server, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/v1/stories", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization,if-match")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestPreflightFromAllowedOriginSucceedsWithoutAuth(t *testing.T) {
	srv := corsServer(t, allowedOrigin)
	res := preflight(t, srv, allowedOrigin)

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	for _, h := range []string{"Authorization", "Content-Type", "If-Match"} {
		if !headerLists(res.Header.Get("Access-Control-Allow-Headers"), h) {
			t.Errorf("Access-Control-Allow-Headers %q is missing %q",
				res.Header.Get("Access-Control-Allow-Headers"), h)
		}
	}
	if got := res.Header.Get("Vary"); !headerLists(got, "Origin") {
		t.Errorf("Vary = %q, want it to list Origin", got)
	}
}

func TestPreflightFromUnknownOriginGetsNoGrant(t *testing.T) {
	srv := corsServer(t, allowedOrigin)
	res := preflight(t, srv, "https://evil.example")

	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want it absent", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Access-Control-Allow-Methods = %q, want it absent", got)
	}
	if res.StatusCode == http.StatusNoContent {
		t.Errorf("status = 204, want the request to fall through unanswered")
	}
}

func TestActualRequestCarriesTheOriginHeader(t *testing.T) {
	reset(t)
	srv := corsServer(t, allowedOrigin)
	newStory(t, alice, "Salt and Sextant")

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/stories", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", allowedOrigin)
	req.Header.Set("X-User-ID", alice)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want it absent", got)
	}
}

func TestEmptyAllowlistGrantsNoOrigin(t *testing.T) {
	srv := corsServer(t)
	res := preflight(t, srv, allowedOrigin)

	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want it absent", got)
	}
}

func TestPlainOptionsIsNotTreatedAsPreflight(t *testing.T) {
	srv := corsServer(t, allowedOrigin)
	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/v1/stories", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", allowedOrigin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNoContent {
		t.Fatalf("status = 204, want the router's own answer for a non-preflight OPTIONS")
	}
}

func headerLists(value, item string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), item) {
			return true
		}
	}
	return false
}
