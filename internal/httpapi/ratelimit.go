package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
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

// allow spends one token from the bucket named key. factor scales the
// configured rate for buckets that several callers share.
func (l *limiter) allow(key string, write bool, factor int) bool {
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
		reads := max(l.cfg.ReadsPerMinute, 1) * max(factor, 1)
		writes := max(l.cfg.WritesPerMinute, 1) * max(factor, 1)
		b = &bucket{
			read:  rate.NewLimiter(rate.Every(time.Minute/time.Duration(reads)), reads),
			write: rate.NewLimiter(rate.Every(time.Minute/time.Duration(writes)), writes),
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

// A bucket several callers share is sized as a multiple of one caller's rate.
const (
	// sharedAddressFactor sizes the bucket for requests that present a
	// credential, since several signed-in people can sit behind one address.
	sharedAddressFactor = 4
	// serviceFactor sizes the one bucket all service-token calls share.
	serviceFactor = 20
)

// withRateLimit throttles before authentication, so it keys only on things
// the caller cannot choose: the source address, or a verified service token.
// The per-user limit is applied later, in underUserLimit, once a uid is proven.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	if s.limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		key, factor := s.rateKey(r)
		if !s.limiter.allow(key, isWrite(r), factor) {
			tooManyRequests(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// underUserLimit spends from the verified caller's own bucket, answering 429
// itself when it returns false. Call it only with a uid the verifier accepted.
func (s *Server) underUserLimit(w http.ResponseWriter, r *http.Request, uid string) bool {
	if s.limiter == nil || s.limiter.allow(userKey(uid), isWrite(r), 1) {
		return true
	}
	tooManyRequests(w)
	return false
}

func isWrite(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(60))
	// Set here because withJSON sits inside the limiter and may not have run.
	w.Header().Set("Content-Type", "application/json")
	writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
}

// rateKey names the pre-authentication bucket and its size. Header text is
// never part of a key: a caller who varies it must not get a fresh bucket.
func (s *Server) rateKey(r *http.Request) (string, int) {
	// Service calls all arrive from the caller's shared egress addresses.
	if s.auth.IsService(r) {
		return "service", serviceFactor
	}
	if presentsCredential(r) {
		return "auth-ip:" + addressKey(r), sharedAddressFactor
	}
	return "ip:" + addressKey(r), 1
}

func presentsCredential(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" || r.Header.Get("X-User-ID") != "" ||
		r.Header.Get("X-Service-Token") != ""
}

// userKey bounds the key size: a Firebase uid is short, but a dev or
// service-asserted one is whatever the caller sent.
func userKey(uid string) string {
	if len(uid) > 128 {
		sum := sha256.Sum256([]byte(uid))
		return "uid:" + hex.EncodeToString(sum[:])
	}
	return "uid:" + uid
}

// addressKey collapses an IPv6 address to its /64, which is what one
// subscriber is routinely assigned and can rotate through freely.
func addressKey(r *http.Request) string {
	addr, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return "unknown"
	}
	addr = addr.Unmap()
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
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
		last := strings.TrimSpace(parts[len(parts)-1])
		// Anything that is not an address is ignored, so it can be neither a
		// limiter key nor a log value of the caller's choosing.
		if _, err := netip.ParseAddr(last); err == nil {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
