package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// This service had no request logging at all: startup and the migration lock
// were the only things it ever wrote. When a 500 happened there was nothing to
// investigate it with beyond Cloud Run's own access log, which knows the
// status but not the error.
//
// Everything here goes to stderr as JSON, which is what Cloud Run's logging
// agent parses into structured entries.

type ctxKey int

const requestIDKey ctxKey = iota

// recorder captures the status for the access log. A handler that never calls
// WriteHeader has answered 200, which is what net/http does implicitly.
type recorder struct {
	http.ResponseWriter
	status int
	// err holds whatever respond() decided was a server fault, so the access
	// log line carries the reason rather than just the number.
	err error
	// uid is filled in by the authentication helper, so the access log names
	// the caller without paying for a second token verification here.
	uid string
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// logError attaches the underlying error to the request being served, so the
// generic body the client receives and the detail an operator needs come apart
// cleanly. Safe to call with a ResponseWriter that is not a recorder — a
// handler invoked outside the middleware simply logs nothing extra.
func logError(w http.ResponseWriter, e error) {
	if r, ok := w.(*recorder); ok {
		r.err = e
	}
}

// logUser records who the request authenticated as.
func logUser(w http.ResponseWriter, uid string) {
	if r, ok := w.(*recorder); ok {
		r.uid = uid
	}
}

// RequestID returns the id assigned to the request being served, for a handler
// or store call that wants to correlate its own logging.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// withLogging emits one line per request. It is the outermost middleware so
// that a request rejected by the rate limiter or by CORS is logged too — the
// rejections are exactly what an operator needs to see during an attack.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The probe runs every few seconds and says nothing an operator wants.
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		id := requestID(r)
		rec := &recorder{ResponseWriter: w}
		rec.Header().Set("X-Request-ID", id)
		start := time.Now()

		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		attrs := []any{
			"requestId", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
			// The caller, not the credential: never the token itself. Empty
			// on the anonymous routes, which never resolve one.
			"uid", rec.uid,
			"ip", clientIP(r),
		}
		switch {
		case rec.err != nil:
			// The one case with a cause worth keeping. The client saw only
			// "internal server error".
			slog.Error("request failed", append(attrs, "error", rec.err.Error())...)
		case rec.status >= 500:
			slog.Error("request failed", attrs...)
		case rec.status >= 400:
			slog.Warn("request rejected", attrs...)
		default:
			slog.Info("request", attrs...)
		}
	})
}

// requestID honours the id the caller or the load balancer already assigned so
// a trace survives across services, and mints one otherwise. An inbound value
// is bounded and only ever logged, never interpreted.
func requestID(r *http.Request) string {
	for _, h := range []string{"X-Request-ID", "X-Cloud-Trace-Context"} {
		if v := r.Header.Get(h); v != "" {
			if len(v) > 128 {
				v = v[:128]
			}
			return v
		}
	}
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
