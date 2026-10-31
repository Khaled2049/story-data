package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

func (s *Server) storyVoice(w http.ResponseWriter, r *http.Request, uid, storyID string, p []string) {
	w.Header().Set("Cache-Control", "no-store")
	if len(p) == 3 && p[2] == "agent-note" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		// The verified service token, plus the asserted user and story ownership
		// gate in the store, is required for this unapproved write path.
		if r.Header.Get("X-Service-Token") == "" {
			writeError(w, http.StatusForbidden, "service identity required")
			return
		}
		rev, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("If-Match")), 10, 64)
		if err != nil || rev < 0 {
			writeError(w, http.StatusPreconditionRequired, "If-Match must be a nonnegative profile revision")
			return
		}
		var op store.AgentVoiceOperation
		if !decode(w, r, &op) {
			return
		}
		x, err := s.store.AgentVoiceNote(r.Context(), storyID, uid, rev, op)
		respond(w, x, err)
		return
	}
	if len(p) == 3 && p[2] == "history" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		x, err := s.store.ListStoryVoiceChanges(r.Context(), storyID, uid)
		respond(w, x, err)
		return
	}
	if len(p) == 5 && p[2] == "history" && p[4] == "undo" {
		if !uuidPath(w, p[3]) {
			return
		}
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		x, err := s.store.UndoStoryVoiceChange(r.Context(), storyID, uid, p[3], rev)
		respond(w, x, err)
		return
	}
	if len(p) != 2 {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		x, err := s.store.GetStoryVoice(r.Context(), storyID, uid)
		respond(w, x, err)
	case http.MethodPut:
		rev, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("If-Match")), 10, 64)
		if err != nil || rev < 0 {
			writeError(w, http.StatusPreconditionRequired, "If-Match must be a nonnegative profile revision")
			return
		}
		var in store.StoryVoiceInput
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.PutStoryVoice(r.Context(), storyID, uid, rev, in)
		respond(w, x, err)
	default:
		method(w)
	}
}
