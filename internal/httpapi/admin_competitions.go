package httpapi

import (
	"log/slog"
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
	rest := strings.TrimPrefix(r.URL.Path, "/v1/admin/competitions/")
	if rest == "settle-due" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		res, err := s.store.SettleDue(r.Context(), uid)
		if err == nil && res.Err() != nil {
			// Failures are reported by id only; the detail belongs in the log.
			slog.Error("admin settle-due left competitions unsettled", "error", res.Err())
		}
		respond(w, res, err)
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	if !uuidPath(w, id) {
		return
	}
	if action == "settle" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		s.sweepDuePhases(r)
		c, err := s.store.SettleAdminCompetition(r.Context(), true, uid, id)
		respond(w, c, err)
		return
	}
	if action != "" {
		notFound(w)
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
