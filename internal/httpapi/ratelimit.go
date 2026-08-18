package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimit bounds how fast one caller can hit the service. It is a blast
// radius control, not a quota: buckets live in the instance's memory, so with
// several Cloud Run instances a determined caller gets one bucket per instance
// they land on. The durable per-user ceilings that must hold platform-wide
// (guestbook, book club, comments, ratings, drafts) live in the database
// alongside the writes they meter. What this stops is the case that needs no
// account at all — an anonymous loop saturating the connection pool or
// inflating a public counter — and it stops it before a request reaches
// PostgreSQL, which is the resource actually in short supply.
type RateLimit struct {
	// Reads applies to GET, HEAD and OPTIONS; Writes to every other method,
	// which is where a request costs a transaction. Both are per minute, with
	// a burst of the same size. Zero disables that half.
	ReadsPerMinute  int
	WritesPerMinute int
}

// DefaultRateLimit is sized for a reader browsing quickly with a warm page
// making several parallel requests, not for a script. A person will not notice
// it; the view-count loop in the security review dies at request 301.
var DefaultRateLimit = RateLimit{ReadsPerMinute: 300, WritesPerMinute: 60}

// Disabled turns the middleware into a pass-through. Tests and local stacks
// use it; nothing in production should.
func (l RateLimit) Disabled() bool { return l.ReadsPerMinute <= 0 && l.WritesPerMinute <= 0 }

// bucket is one caller's pair of limiters plus the last time we saw them, so
// idle callers can be swept. Without the sweep the map is itself an unbounded
// allocation keyed on attacker-controlled input — the very thing being fixed.
type bucket struct {
	read  *rate.Limiter
	write *rate.Limiter
	seen  time.Time
}

type limiter struct {
	cfg     RateLimit
	mu      sync.Mutex
	buckets map[string]*bucket
	// now is a seam for tests; production leaves it nil and uses time.Now.
	now func() time.Time
}

const bucketTTL = 10 * time.Minute

func newLimiter(cfg RateLimit) *limiter {
	return &limiter{cfg: cfg, buckets: map[string]*bucket{}}
}

func (l *limiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// allow spends one token from the caller's bucket for this kind of request.
func (l *limiter) allow(key string, write bool) bool {
	perMinute := l.cfg.ReadsPerMinute
	if write {
		perMinute = l.cfg.WritesPerMinute
	}
	if perMinute <= 0 {
		return true
	}
	now := l.clock()

	l.mu.Lock()
	b := l.buckets[key]
	if b == nil {
		every := rate.Every(time.Minute / time.Duration(max(l.cfg.ReadsPerMinute, 1)))
		everyWrite := rate.Every(time.Minute / time.Duration(max(l.cfg.WritesPerMinute, 1)))
		b = &bucket{
			read:  rate.NewLimiter(every, max(l.cfg.ReadsPerMinute, 1)),
			write: rate.NewLimiter(everyWrite, max(l.cfg.WritesPerMinute, 1)),
		}
		l.buckets[key] = b
		l.sweepLocked(now)
	}
	b.seen = now
	lim := b.read
	if write {
		lim = b.write
	}
	l.mu.Unlock()

	return lim.AllowN(now, 1)
}

// sweepLocked drops buckets nobody has used for a while. It runs on insert
// rather than on a timer so the middleware owns no goroutine, and it is cheap
// because it only ever runs when the map is growing.
func (l *limiter) sweepLocked(now time.Time) {
	if len(l.buckets) < 1024 {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.seen) > bucketTTL {
			delete(l.buckets, k)
		}
	}
}

// withRateLimit keys on the authenticated uid when there is one, so a user
// behind a shared NAT is not throttled by their neighbours, and on the client
// IP otherwise, which is all an anonymous caller has. Identifying the caller
// must not cost a token verification on every request, so this reads the
// header rather than calling the verifier: an attacker can vary the value, but
// only to keys they could reach anyway by varying source addresses.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	if s.limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		write := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		if !s.limiter.allow(rateKey(r), write) {
			w.Header().Set("Retry-After", strconv.Itoa(60))
			// Set here rather than relying on withJSON, which sits inside this
			// middleware and never runs for a rejected request.
			w.Header().Set("Content-Type", "application/json")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateKey identifies the caller for throttling. A bearer token is hashed to a
// stable key without being verified — throttling is not authorization, and a
// forged token buys nothing but its own bucket.
func rateKey(r *http.Request) string {
	if t := r.Header.Get("Authorization"); t != "" {
		return "auth:" + t
	}
	if u := r.Header.Get("X-User-ID"); u != "" {
		return "uid:" + u
	}
	return "ip:" + clientIP(r)
}

// clientIP reads the LAST entry of X-Forwarded-For, not the first. Every
// request reaches this service through Google's front end, which appends the
// address it observed; anything earlier in the list was supplied by the caller
// and can say whatever they like. Taking the first entry would let one client
// mint a fresh bucket per request.
//
// This is correct for the direct *.run.app URL the service is deployed behind
// today. Putting an external load balancer in front (which is what attaching
// Cloud Armor would require) makes the last entry the balancer's own address
// and collapses every caller into one bucket — revisit this function if that
// happens.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
