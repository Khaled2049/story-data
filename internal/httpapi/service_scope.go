package httpapi

import (
	"net/http"
	"strings"
)

// serviceRoutes is everything a backend service may call while asserting a
// user: the MCP tools and the Story Assistant, read from taleTribe-agents'
// StoryDataClient. "*" matches one non-empty path segment. Keep it to what
// those callers use, so a stolen service identity reaches no more.
var serviceRoutes = []struct{ method, pattern string }{
	{http.MethodGet, "/v1/profiles/me"},
	{http.MethodGet, "/v1/stories"},
	{http.MethodPost, "/v1/stories"},
	{http.MethodGet, "/v1/stories/*"},
	{http.MethodGet, "/v1/stories/*/chapters"},
	{http.MethodPost, "/v1/stories/*/chapters"},
	{http.MethodGet, "/v1/stories/*/chapters/*"},
	{http.MethodPatch, "/v1/stories/*/chapters/*"},
	{http.MethodGet, "/v1/stories/*/characters"},
	{http.MethodGet, "/v1/stories/*/characters/*"},
	{http.MethodGet, "/v1/stories/*/places"},
	{http.MethodGet, "/v1/stories/*/places/*"},
	{http.MethodGet, "/v1/stories/*/plots"},
	{http.MethodGet, "/v1/stories/*/plots/*"},
	{http.MethodGet, "/v1/stories/*/voice-profile"},
	{http.MethodPost, "/v1/stories/*/voice-profile/agent-note"},
	{http.MethodGet, "/v1/stories/*/assistant-threads/*"},
	{http.MethodGet, "/v1/stories/*/assistant-threads/*/messages"},
}

func serviceRouteAllowed(method, path string) bool {
	got := pathSegments(path)
	for _, route := range serviceRoutes {
		if route.method == method && segmentsMatch(pathSegments(route.pattern), got) {
			return true
		}
	}
	return false
}

func pathSegments(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}

func segmentsMatch(pattern, got []string) bool {
	if len(pattern) != len(got) {
		return false
	}
	for i, p := range pattern {
		if got[i] == "" || (p != "*" && p != got[i]) {
			return false
		}
	}
	return true
}

// serviceStoryID returns the story a service request is scoped to, if any.
func serviceStoryID(path string) (string, bool) {
	p := pathSegments(path)
	if len(p) >= 3 && p[0] == "v1" && p[1] == "stories" {
		return p[2], true
	}
	return "", false
}

// withServiceScope resolves the service credential once, then refuses a bad
// one and any route outside serviceRoutes before routing. Applying it here
// rather than in user() covers the public and optional-viewer routes too.
func (s *Server) withServiceScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = s.auth.ResolveService(r)
		call := s.auth.Service(r)
		if !call.Present {
			next.ServeHTTP(w, r)
			return
		}
		if call.Err != nil {
			logUser(w, "", "service", "")
			writeError(w, http.StatusUnauthorized, call.Err.Error())
			return
		}
		logUser(w, strings.TrimSpace(r.Header.Get("X-User-ID")), "service", call.Caller)
		if !serviceRouteAllowed(r.Method, r.URL.Path) {
			writeError(w, http.StatusForbidden, "not available to service callers")
			return
		}
		next.ServeHTTP(w, r)
	})
}
