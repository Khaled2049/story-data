package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/public/profiles"), "/"), "/")
	if len(p) == 1 && p[0] == "" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		limit, err := profileLimit(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ids := strings.FieldsFunc(r.URL.Query().Get("ids"), func(r rune) bool { return r == ',' })
		if r.URL.Query().Get("query") != "" && len(ids) != 0 {
			writeError(w, http.StatusBadRequest, "query and ids cannot be combined")
			return
		}
		x, err := s.store.ListPublicProfiles(r.Context(), r.URL.Query().Get("query"), ids, limit)
		respond(w, x, err)
		return
	}
	if len(p) == 1 && p[0] != "" && r.Method == http.MethodGet {
		x, err := s.store.GetPublicProfile(r.Context(), p[0])
		respond(w, x, err)
		return
	}
	notFound(w)
}

func (s *Server) myProfile(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		x, err := s.store.GetPublicProfile(r.Context(), uid)
		respond(w, x, err)
	case http.MethodPut:
		var in store.ProfileInput
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.UpsertPublicProfile(r.Context(), uid, in)
		if err == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, err)
		}
	case http.MethodPatch:
		var in store.ProfileInput
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.PatchPublicProfile(r.Context(), uid, in)
		respond(w, x, err)
	default:
		method(w)
	}
}
func profileLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 20, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 50 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}
