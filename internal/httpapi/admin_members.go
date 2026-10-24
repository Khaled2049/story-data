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

// adminMember serves GET /v1/admin/members/{userId}/usage and
// PUT/DELETE /v1/admin/members/{userId}/ban.
func (s *Server) adminMember(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.admin(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/admin/members/"), "/")
	if len(p) != 2 || p[0] == "" || len(p[0]) > 128 {
		notFound(w)
		return
	}
	switch {
	case p[1] == "usage" && r.Method == http.MethodGet:
		q := r.URL.Query()
		usage, err := s.store.MemberUsage(r.Context(), true, p[0], q.Get("from"), q.Get("to"))
		respond(w, usage, err)
	case p[1] == "ban" && r.Method == http.MethodPut:
		var in struct {
			Reason string `json:"reason"`
		}
		if !decode(w, r, &in) {
			return
		}
		respond(w, nil, s.store.BanUser(r.Context(), true, actor, p[0], in.Reason))
	case p[1] == "ban" && r.Method == http.MethodDelete:
		respond(w, nil, s.store.UnbanUser(r.Context(), true, p[0]))
	case p[1] == "usage" || p[1] == "ban":
		method(w)
	default:
		notFound(w)
	}
}
