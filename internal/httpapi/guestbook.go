package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func (s *Server) publicGuestbook(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/public/guestbooks/"), "/"), "/")
	if len(p) < 2 || p[0] == "" || p[1] != "entries" {
		notFound(w)
		return
	}
	viewer := s.optionalUser(r)
	if len(p) == 2 && r.Method == http.MethodGet {
		limit, err := guestbookLimit(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}
		x, err := s.store.ListGuestbookEntries(r.Context(), p[0], viewer, r.URL.Query().Get("cursor"), limit)
		respond(w, x, err)
		return
	}
	if len(p) == 4 && p[2] != "" && p[3] == "replies" && r.Method == http.MethodGet {
		if _, err := uuid.Parse(p[2]); err != nil {
			notFound(w)
			return
		}
		x, err := s.store.ListGuestbookReplies(r.Context(), p[0], p[2], viewer)
		respond(w, x, err)
		return
	}
	notFound(w)
}

func (s *Server) guestbook(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/guestbooks/"), "/"), "/")
	if len(p) < 2 || p[0] == "" || p[1] != "entries" {
		notFound(w)
		return
	}
	owner := p[0]
	if len(p) == 2 && r.Method == http.MethodPost {
		var in struct {
			Content string `json:"content"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.CreateGuestbookEntry(r.Context(), owner, uid, in.Content)
		if err == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, err)
		}
		return
	}
	if len(p) < 3 {
		notFound(w)
		return
	}
	entry := p[2]
	if _, err := uuid.Parse(entry); err != nil {
		notFound(w)
		return
	}
	if len(p) == 3 && r.Method == http.MethodDelete {
		respond(w, nil, s.store.DeleteGuestbookEntry(r.Context(), owner, entry, uid))
		return
	}
	if len(p) == 4 && p[3] == "votes" && r.Method == http.MethodPut {
		var in struct {
			Vote string `json:"vote"`
		}
		if !decode(w, r, &in) {
			return
		}
		respond(w, nil, s.store.SetGuestbookVote(r.Context(), owner, entry, "", uid, in.Vote))
		return
	}
	if len(p) == 4 && p[3] == "replies" && r.Method == http.MethodPost {
		var in struct {
			Content  string `json:"content"`
			ParentID string `json:"parentId"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, err := s.store.CreateGuestbookReply(r.Context(), owner, entry, uid, in.ParentID, in.Content)
		if err == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, err)
		}
		return
	}
	if len(p) == 5 && p[3] == "replies" {
		reply := p[4]
		if _, err := uuid.Parse(reply); err != nil {
			notFound(w)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var in struct {
				Content string `json:"content"`
			}
			if !decode(w, r, &in) {
				return
			}
			x, err := s.store.UpdateGuestbookReply(r.Context(), owner, entry, reply, uid, in.Content)
			respond(w, x, err)
		case http.MethodDelete:
			respond(w, nil, s.store.DeleteGuestbookReply(r.Context(), owner, entry, reply, uid))
		default:
			method(w)
		}
		return
	}
	if len(p) == 6 && p[3] == "replies" && p[5] == "votes" && r.Method == http.MethodPut {
		if _, err := uuid.Parse(p[4]); err != nil {
			notFound(w)
			return
		}
		var in struct {
			Vote string `json:"vote"`
		}
		if !decode(w, r, &in) {
			return
		}
		respond(w, nil, s.store.SetGuestbookVote(r.Context(), owner, entry, p[4], uid, in.Vote))
		return
	}
	notFound(w)
}

func (s *Server) myFollows(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	following, e := s.store.ListFollowing(r.Context(), uid)
	if e != nil {
		respond(w, nil, e)
		return
	}
	followers, e := s.store.ListFollowers(r.Context(), uid)
	respond(w, map[string][]string{"following": following, "followers": followers}, e)
}
func (s *Server) profileAction(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/profiles/"), "/"), "/")
	if len(p) != 2 || p[1] != "follow" || p[0] == "" {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodPut:
		respond(w, nil, s.store.Follow(r.Context(), uid, p[0], true))
	case http.MethodDelete:
		respond(w, nil, s.store.Follow(r.Context(), uid, p[0], false))
	default:
		method(w)
	}
}
func (s *Server) optionalUser(r *http.Request) string {
	if r.Header.Get("Authorization") == "" && r.Header.Get("X-User-ID") == "" {
		return ""
	}
	uid, err := s.auth.UserID(r.Context(), r)
	if err != nil {
		return ""
	}
	return uid
}
func guestbookLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 10, nil
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 1 || n > 50 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}
