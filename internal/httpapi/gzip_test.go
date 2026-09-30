package httpapi_test

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kh1011/novelsync-story-data/internal/auth"
	"github.com/kh1011/novelsync-story-data/internal/httpapi"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

const allowedOrigin = "https://app.example"

func serve(t *testing.T, r *http.Request) *http.Response {
	t.Helper()
	h := httpapi.New(store.New(nil), auth.New("dev", "", ""), []string{allowedOrigin}, httpapi.RateLimit{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result()
}

func badListRequest(acceptEncoding string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/public/stories?limit=abc", nil)
	if acceptEncoding != "" {
		r.Header.Set("Accept-Encoding", acceptEncoding)
	}
	return r
}

func hasToken(header, token string) bool {
	for part := range strings.SplitSeq(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func TestGzipCompressesWhenAccepted(t *testing.T) {
	res := serve(t, badListRequest("br, gzip"))
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if err := json.NewDecoder(zr).Decode(&body); err != nil {
		t.Fatalf("decompressed body is not JSON: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("body = %v, want an error message", body)
	}
}

func TestGzipLeavesPlainClientsAlone(t *testing.T) {
	for _, accept := range []string{"", "identity", "gzip;q=0", "br"} {
		res := serve(t, badListRequest(accept))
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if got := res.Header.Get("Content-Encoding"); got != "" {
			t.Errorf("Accept-Encoding %q: Content-Encoding = %q, want none", accept, got)
		}
		if !json.Valid(raw) {
			t.Errorf("Accept-Encoding %q: body is not plain JSON: %q", accept, raw)
		}
	}
}

func TestGzipAcceptsWeightedGzip(t *testing.T) {
	res := serve(t, badListRequest("gzip;q=0.5"))
	res.Body.Close()
	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
}

func TestResponsesVaryOnEncodingAndOrigin(t *testing.T) {
	for _, origin := range []string{"", allowedOrigin, "https://elsewhere.example"} {
		r := badListRequest("gzip")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		res := serve(t, r)
		res.Body.Close()
		vary := strings.Join(res.Header.Values("Vary"), ",")
		if !hasToken(vary, "Accept-Encoding") || !hasToken(vary, "Origin") {
			t.Errorf("Origin %q: Vary = %q, want Accept-Encoding and Origin", origin, vary)
		}
	}
}

func TestPreflightIsNotCompressed(t *testing.T) {
	r := httptest.NewRequest(http.MethodOptions, "/v1/public/stories", nil)
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Accept-Encoding", "gzip")
	res := serve(t, r)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	if got := res.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q on a 204, want none", got)
	}
	if len(raw) != 0 {
		t.Fatalf("204 carried %d body bytes", len(raw))
	}
}

func TestFailedListIsNotCacheable(t *testing.T) {
	res := serve(t, badListRequest(""))
	res.Body.Close()
	if got := res.Header.Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q on a 400, want none", got)
	}
}
