package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kh1011/novelsync-story-data/internal/auth"
)

func limitedTestServer(rl RateLimit) *Server {
	return &Server{auth: auth.New("production", "", "service-secret"), limiter: newLimiter(rl)}
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// The bucket map must not grow with header text a caller invents, whatever
// its size: that map is memory an anonymous client would otherwise control.
func TestLimiterKeysIgnoreCallerSuppliedHeaders(t *testing.T) {
	s := limitedTestServer(RateLimit{ReadsPerMinute: 100000, WritesPerMinute: 100000})
	h := s.withRateLimit(http.HandlerFunc(okHandler))
	big := strings.Repeat("x", 4096)

	for i := 0; i < 500; i++ {
		r := httptest.NewRequest("GET", "/v1/public/stories", nil)
		r.Header.Set("X-Forwarded-For", "203.0.113.70")
		r.Header.Set("Authorization", fmt.Sprintf("Bearer %s-%d", big, i))
		r.Header.Set("X-User-ID", fmt.Sprintf("%s-%d", big, i))
		r.Header.Set("X-Service-Token", fmt.Sprintf("wrong-%d", i))
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if n := len(s.limiter.buckets); n != 1 {
		t.Fatalf("500 forged credentials from one address made %d buckets, want 1", n)
	}
	for key := range s.limiter.buckets {
		if len(key) > 64 {
			t.Errorf("bucket key is %d bytes; keys must be bounded", len(key))
		}
	}
}

func TestRateKey(t *testing.T) {
	s := limitedTestServer(DefaultRateLimit)
	req := func(remote string, headers map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/v1/stories", nil)
		r.RemoteAddr = remote
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name   string
		r      *http.Request
		key    string
		factor int
	}{
		{"anonymous", req("10.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.5"}),
			"ip:203.0.113.5", 1},
		{"credential", req("10.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.5", "Authorization": "Bearer x"}),
			"auth-ip:203.0.113.5", sharedAddressFactor},
		{"valid service token", req("10.0.0.1:1", map[string]string{"X-Service-Token": "service-secret", "X-User-ID": "u"}),
			"service", serviceFactor},
		{"wrong service token", req("10.0.0.1:1", map[string]string{"X-Service-Token": "nope", "X-User-ID": "u"}),
			"auth-ip:10.0.0.1", sharedAddressFactor},
		{"one IPv6 /64 is one caller", req("10.0.0.1:1", map[string]string{"X-Forwarded-For": "2001:db8:1:2:aaaa:bbbb:cccc:dddd"}),
			"ip:2001:db8:1:2::/64", 1},
		{"IPv4-mapped", req("10.0.0.1:1", map[string]string{"X-Forwarded-For": "::ffff:203.0.113.5"}),
			"ip:203.0.113.5", 1},
		{"a forwarded value that is not an address is ignored", req("10.0.0.1:1", map[string]string{"X-Forwarded-For": "not-an-ip"}),
			"ip:10.0.0.1", 1},
		{"unparseable peer", req("garbage", nil), "ip:unknown", 1},
	}
	for _, c := range cases {
		key, factor := s.rateKey(c.r)
		if key != c.key || factor != c.factor {
			t.Errorf("%s: rateKey = %q x%d, want %q x%d", c.name, key, factor, c.key, c.factor)
		}
	}
}

func TestUserKeyIsBounded(t *testing.T) {
	if got := userKey("abc"); got != "uid:abc" {
		t.Errorf("userKey(abc) = %q", got)
	}
	long := strings.Repeat("u", 5000)
	if got := userKey(long); len(got) > 80 || got == userKey(long+"x") {
		t.Errorf("a long uid must hash to a short, distinct key; got %d bytes", len(got))
	}
}

// The access log must say which credential proved the uid, so a service
// asserting a user can be told apart from that user's own token.
func TestAccessLogNamesTheCredential(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := limitedTestServer(RateLimit{})
	authed := s.withLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logUser(w, "user-1", s.auth.Class(r))
	}))
	anonymous := s.withLogging(http.HandlerFunc(okHandler))

	r := httptest.NewRequest("GET", "/v1/stories", nil)
	r.Header.Set("X-Service-Token", "service-secret")
	authed.ServeHTTP(httptest.NewRecorder(), r)
	authed.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/stories", nil))
	anonymous.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/public/stories", nil))

	want := []string{"service", "firebase", "anonymous"}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d log lines, want %d: %s", len(lines), len(want), buf.String())
	}
	for i, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %d is not JSON: %v", i, err)
		}
		if entry["auth"] != want[i] {
			t.Errorf("log line %d auth = %v, want %q", i, entry["auth"], want[i])
		}
		if strings.Contains(line, "service-secret") {
			t.Errorf("log line %d contains the service token", i)
		}
	}
}
