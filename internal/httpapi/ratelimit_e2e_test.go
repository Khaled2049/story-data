package httpapi_test

// Request throttling.
//
// The service is invokable by anyone on the internet with no credential, and
// the resource that runs out first is the database connection pool, not CPU.
// These tests use their own server because the rest of the suite fires
// thousands of requests from a handful of identities and runs with the limiter
// switched off.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kh1011/novelsync-story-data/internal/auth"
	"github.com/kh1011/novelsync-story-data/internal/httpapi"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

func limitedServer(t *testing.T, rl httpapi.RateLimit) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(httpapi.New(store.New(testPool),
		auth.New("dev", "", testServiceToken), nil, rl))
	t.Cleanup(s.Close)
	return s
}

// hit sends one request to srv as the given caller and returns the status.
func hit(t *testing.T, srv *httptest.Server, method, path string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	res.Body.Close()
	return res
}

func TestRateLimitStopsAFlood(t *testing.T) {
	reset(t)
	srv := limitedServer(t, httpapi.RateLimit{ReadsPerMinute: 5, WritesPerMinute: 2})
	anon := map[string]string{"X-Forwarded-For": "203.0.113.9"}

	for i := 0; i < 5; i++ {
		if res := hit(t, srv, "GET", "/v1/public/stories", anon); res.StatusCode != http.StatusOK {
			t.Fatalf("read %d = %d, want 200", i+1, res.StatusCode)
		}
	}
	res := hit(t, srv, "GET", "/v1/public/stories", anon)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("read 6 = %d, want 429", res.StatusCode)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Error("a 429 must tell the caller when to come back")
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("429 Content-Type = %q; clients parse every error as JSON", ct)
	}
}

// Writes get their own, tighter budget: a write costs a transaction, a read
// often costs one query.
func TestRateLimitBudgetsWritesSeparately(t *testing.T) {
	reset(t)
	srv := limitedServer(t, httpapi.RateLimit{ReadsPerMinute: 100, WritesPerMinute: 2})
	anon := map[string]string{"X-Forwarded-For": "203.0.113.10"}

	// Two writes are allowed. The path does not have to exist — the limiter
	// runs before routing, which is the point of doing it in middleware.
	for i := 0; i < 2; i++ {
		if res := hit(t, srv, "POST", "/v1/public/stories/x/views", anon); res.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("write %d was throttled inside its budget", i+1)
		}
	}
	if res := hit(t, srv, "POST", "/v1/public/stories/x/views", anon); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("write 3 = %d, want 429", res.StatusCode)
	}
	// Reads are on their own budget and unaffected.
	if res := hit(t, srv, "GET", "/v1/public/stories", anon); res.StatusCode != http.StatusOK {
		t.Errorf("read after exhausting the write budget = %d, want 200", res.StatusCode)
	}
}

// One caller's flood must not throttle anybody else, or the limiter becomes
// the denial of service it was added to prevent.
func TestRateLimitIsPerCaller(t *testing.T) {
	reset(t)
	srv := limitedServer(t, httpapi.RateLimit{ReadsPerMinute: 2, WritesPerMinute: 2})
	flooder := map[string]string{"X-Forwarded-For": "203.0.113.11"}

	for i := 0; i < 3; i++ {
		hit(t, srv, "GET", "/v1/public/stories", flooder)
	}
	if res := hit(t, srv, "GET", "/v1/public/stories", flooder); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the flooder was not throttled: %d", res.StatusCode)
	}

	neighbour := map[string]string{"X-Forwarded-For": "198.51.100.22"}
	if res := hit(t, srv, "GET", "/v1/public/stories", neighbour); res.StatusCode != http.StatusOK {
		t.Errorf("a different address was throttled by its neighbour: %d", res.StatusCode)
	}
	signedIn := map[string]string{"X-Forwarded-For": "203.0.113.11", "X-User-ID": alice}
	if res := hit(t, srv, "GET", "/v1/public/stories", signedIn); res.StatusCode != http.StatusOK {
		t.Errorf("a signed-in user sharing an address was throttled: %d", res.StatusCode)
	}
}

// A caller cannot mint a fresh bucket by prepending their own hops: the entry
// the infrastructure appends is the last one.
func TestRateLimitIgnoresSpoofedForwardedFor(t *testing.T) {
	reset(t)
	srv := limitedServer(t, httpapi.RateLimit{ReadsPerMinute: 3})

	for i := 0; i < 3; i++ {
		hit(t, srv, "GET", "/v1/public/stories",
			map[string]string{"X-Forwarded-For": "10.0.0.1, 203.0.113.12"})
	}
	res := hit(t, srv, "GET", "/v1/public/stories",
		map[string]string{"X-Forwarded-For": "10.9.9.9, 203.0.113.12"})
	if res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("a spoofed leading hop bought a new bucket: %d", res.StatusCode)
	}
}

// The liveness probe must answer even while a caller is being throttled, or a
// flood takes the instance out of rotation.
func TestRateLimitExemptsHealth(t *testing.T) {
	reset(t)
	srv := limitedServer(t, httpapi.RateLimit{ReadsPerMinute: 1})
	probe := map[string]string{"X-Forwarded-For": "203.0.113.13"}

	for i := 0; i < 5; i++ {
		if res := hit(t, srv, "GET", "/health", probe); res.StatusCode != http.StatusOK {
			t.Fatalf("health check %d = %d", i+1, res.StatusCode)
		}
	}
}
