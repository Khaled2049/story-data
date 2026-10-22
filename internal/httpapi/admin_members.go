package httpapi

import (
	"net/http"
	"strings"
)

func (s *Server) adminMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.admin(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := s.store.ListAdminMembers(r.Context(), true, q.Get("q"), q.Get("cursor"), limit)
	respond(w, page, err)
}

// adminMemberUsage serves GET /v1/admin/members/{userId}/usage.
func (s *Server) adminMemberUsage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.admin(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/admin/members/"), "/")
	if len(p) != 2 || p[0] == "" || len(p[0]) > 128 || p[1] != "usage" {
		notFound(w)
		return
	}
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	q := r.URL.Query()
	usage, err := s.store.MemberUsage(r.Context(), true, p[0], q.Get("from"), q.Get("to"))
	respond(w, usage, err)
}
