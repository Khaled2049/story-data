package httpapi

import (
	"net/http"
	"strings"
)

func (s *Server) adminCompetitions(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.admin(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/v1/admin/competitions" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		limit, ok := pageLimit(w, r)
		if !ok {
			return
		}
		s.sweepDuePhases(r)
		q := r.URL.Query()
		page, err := s.store.ListAdminCompetitions(r.Context(), true, q.Get("q"), q.Get("phase"), q.Get("cursor"), limit)
		respond(w, page, err)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/admin/competitions/")
	if !uuidPath(w, id) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.sweepDuePhases(r)
		c, err := s.store.GetAdminCompetition(r.Context(), true, uid, id)
		respond(w, c, err)
	case http.MethodDelete:
		respond(w, nil, s.store.DiscardAdminCompetitionDraft(r.Context(), true, uid, id))
	default:
		method(w)
	}
}
