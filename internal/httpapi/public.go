package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// viewerKey identifies who is counting a view, without storing who they are.
// A signed-in reader is their uid. Everyone else is a keyed hash of the client
// address: the same reader collides with themselves for the day, and the table
// never holds an address that could be read back out. The key is per-process
// and per-day, so the hashes are not stable enough to track anyone across
// deploys either.
func (s *Server) viewerKey(r *http.Request) string {
	if uid := s.optionalUser(r); uid != "" {
		return "u:" + uid
	}
	mac := hmac.New(sha256.New, s.viewSalt)
	mac.Write([]byte(clientIP(r)))
	return "a:" + hex.EncodeToString(mac.Sum(nil)[:16])
}

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
		respond(w, nil, s.store.IncrementPublicStoryViews(r.Context(), storyID, s.viewerKey(r)))
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
