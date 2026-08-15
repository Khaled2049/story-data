package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

func (s *Server) readingHistory(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	if r.URL.Path != "/v1/me/reading-history" {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, err := readingLimit(r)
		if err != nil || limit < 1 || limit > 50 {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 50")
			return
		}
		x, err := s.store.ListReadingHistory(r.Context(), uid, limit)
		respond(w, x, err)
	case http.MethodDelete:
		respond(w, nil, s.store.ClearReadingHistory(r.Context(), uid))
	default:
		method(w)
	}
}

func (s *Server) readingProgress(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	storyID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/me/reading-progress/"), "/")
	if storyID == "" || strings.Contains(storyID, "/") {
		notFound(w)
		return
	}
	if _, err := uuid.Parse(storyID); err != nil {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		x, err := s.store.GetReadingProgress(r.Context(), uid, storyID)
		respond(w, x, err)
	case http.MethodPut:
		var in store.ReadingProgressInput
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.PutReadingProgress(r.Context(), uid, storyID, in)
		respond(w, x, err)
	default:
		method(w)
	}
}
func readingLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 5, nil
	}
	return strconv.Atoi(raw)
}
