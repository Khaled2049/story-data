package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func (s *Server) public(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/public/"), "/"), "/")
	if len(p) == 1 && p[0] == "stories" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if r.URL.Query().Get("limit") != "" && (err != nil || limit < 1) {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		x, err := s.store.ListPublicStories(r.Context(), r.URL.Query().Get("category"), r.URL.Query().Get("cursor"), limit)
		if err != nil && err.Error() == "invalid cursor" {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		respond(w, x, err)
		return
	}
	if len(p) < 2 || p[0] != "stories" || p[1] == "" {
		notFound(w)
		return
	}
	storyID := p[1]
	if _, err := uuid.Parse(storyID); err != nil {
		notFound(w)
		return
	}
	if len(p) == 2 && r.Method == http.MethodGet {
		x, err := s.store.GetPublicStory(r.Context(), storyID)
		respond(w, x, err)
		return
	}
	if len(p) == 3 && p[2] == "views" && r.Method == http.MethodPost {
		respond(w, nil, s.store.IncrementPublicStoryViews(r.Context(), storyID))
		return
	}
	if len(p) == 5 && p[2] == "chapters" && p[4] == "comments" && r.Method == http.MethodGet {
		if _, err := uuid.Parse(p[3]); err != nil {
			notFound(w)
			return
		}
		x, err := s.store.ListPublicComments(r.Context(), storyID, p[3], s.optionalUser(r))
		respond(w, x, err)
		return
	}
	if len(p) == 4 && p[2] == "chapters" && r.Method == http.MethodGet {
		if _, err := uuid.Parse(p[3]); err != nil {
			notFound(w)
			return
		}
		x, err := s.store.GetPublicChapter(r.Context(), storyID, p[3])
		respond(w, x, err)
		return
	}
	notFound(w)
}
