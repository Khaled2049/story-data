package httpapi

import (
	"net/http"
	"strconv"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

func assistantPageLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}

func (s *Server) assistantThreads(w http.ResponseWriter, r *http.Request, uid, storyID string, p []string) {
	if len(p) == 2 {
		s.assistantThreadCollection(w, r, uid, storyID)
		return
	}
	if !uuidPath(w, p[2]) {
		return
	}
	threadID := p[2]
	if len(p) == 3 {
		s.assistantThreadResource(w, r, uid, storyID, threadID)
		return
	}
	if len(p) == 4 && p[3] == "messages" {
		s.assistantMessageCollection(w, r, uid, storyID, threadID)
		return
	}
	if len(p) == 5 && p[3] == "messages" {
		if !uuidPath(w, p[4]) {
			return
		}
		s.assistantMessageResource(w, r, uid, storyID, threadID, p[4])
		return
	}
	notFound(w)
}

func (s *Server) assistantThreadCollection(w http.ResponseWriter, r *http.Request, uid, storyID string) {
	switch r.Method {
	case http.MethodGet:
		limit, err := assistantPageLimit(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		x, err := s.store.ListAssistantThreads(r.Context(), storyID, uid, r.URL.Query().Get("cursor"), limit)
		respond(w, x, err)
	case http.MethodPost:
		var in store.AssistantThreadInput
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.CreateAssistantThread(r.Context(), storyID, uid, in)
		if err == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, err)
		}
	default:
		method(w)
	}
}

func (s *Server) assistantThreadResource(w http.ResponseWriter, r *http.Request, uid, storyID, threadID string) {
	switch r.Method {
	case http.MethodGet:
		x, err := s.store.GetAssistantThread(r.Context(), storyID, threadID, uid)
		respond(w, x, err)
	case http.MethodPatch:
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		var in store.AssistantThreadPatch
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.UpdateAssistantThread(r.Context(), storyID, threadID, uid, rev, in)
		respond(w, x, err)
	case http.MethodDelete:
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		respond(w, nil, s.store.DeleteAssistantThread(r.Context(), storyID, threadID, uid, rev))
	default:
		method(w)
	}
}

func (s *Server) assistantMessageCollection(w http.ResponseWriter, r *http.Request, uid, storyID, threadID string) {
	switch r.Method {
	case http.MethodGet:
		limit, err := assistantPageLimit(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		x, err := s.store.ListAssistantMessages(r.Context(), storyID, threadID, uid, r.URL.Query().Get("cursor"), limit)
		respond(w, x, err)
	case http.MethodPost:
		var in store.AssistantMessageInput
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.CreateAssistantMessage(r.Context(), storyID, threadID, uid, in)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if x.IdempotentReplay {
			write(w, http.StatusOK, x)
		} else {
			write(w, http.StatusCreated, x)
		}
	default:
		method(w)
	}
}

func (s *Server) assistantMessageResource(w http.ResponseWriter, r *http.Request, uid, storyID, threadID, messageID string) {
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	rev, ok := revision(w, r)
	if !ok {
		return
	}
	var in store.AssistantMessagePatch
	if !decode(w, r, &in) {
		return
	}
	x, err := s.store.UpdateAssistantMessage(r.Context(), storyID, threadID, messageID, uid, rev, in)
	respond(w, x, err)
}
