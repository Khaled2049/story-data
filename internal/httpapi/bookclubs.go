package httpapi

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

func (s *Server) bookClubs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/book-clubs" {
		notFound(w)
		return
	}
	if r.Method == http.MethodGet {
		x, e := s.store.ListBookClubs(r.Context(), s.optionalUser(r))
		respond(w, x, e)
		return
	}
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in store.BookClubInput
	if !decode(w, r, &in) {
		return
	}
	x, e := s.store.CreateBookClub(r.Context(), uid, in)
	if e == nil {
		write(w, http.StatusCreated, x)
	} else {
		respond(w, nil, e)
	}
}

func (s *Server) bookClub(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/book-clubs/"), "/"), "/")
	if len(p) == 0 || p[0] == "" {
		notFound(w)
		return
	}
	if _, e := uuid.Parse(p[0]); e != nil {
		notFound(w)
		return
	}
	club := p[0]
	if len(p) == 1 {
		switch r.Method {
		case http.MethodGet:
			x, e := s.store.GetBookClub(r.Context(), club)
			respond(w, x, e)
		case http.MethodPatch:
			uid, ok := s.user(w, r)
			if !ok {
				return
			}
			var in store.BookClubInput
			if !decode(w, r, &in) {
				return
			}
			x, e := s.store.UpdateBookClub(r.Context(), club, uid, in)
			respond(w, x, e)
		case http.MethodDelete:
			uid, ok := s.user(w, r)
			if !ok {
				return
			}
			respond(w, nil, s.store.DeleteBookClub(r.Context(), club, uid))
		default:
			method(w)
		}
		return
	}
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	switch {
	case len(p) == 2 && p[1] == "settings" && r.Method == http.MethodPatch:
		var in store.BookClubSettings
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.UpdateBookClubSettings(r.Context(), club, uid, in)
		respond(w, x, e)
	case len(p) == 3 && p[1] == "members" && p[2] == "me":
		if r.Method == http.MethodPut {
			respond(w, nil, s.store.JoinBookClub(r.Context(), club, uid))
		} else if r.Method == http.MethodDelete {
			respond(w, nil, s.store.LeaveBookClub(r.Context(), club, uid))
		} else {
			method(w)
		}
	case len(p) == 2 && p[1] == "progress" && r.Method == http.MethodGet:
		x, e := s.store.ListClubProgress(r.Context(), club, uid)
		respond(w, x, e)
	case len(p) == 3 && p[1] == "progress" && p[2] == "me" && r.Method == http.MethodPut:
		var in store.ProgressInput
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.PutClubProgress(r.Context(), club, uid, in)
		respond(w, x, e)
	case len(p) == 2 && p[1] == "prompts" && r.Method == http.MethodPost:
		var in store.PromptInput
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.CreatePrompt(r.Context(), club, uid, in)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
	case len(p) == 4 && p[1] == "prompts" && p[3] == "responses" && r.Method == http.MethodPost:
		if _, e := uuid.Parse(p[2]); e != nil {
			notFound(w)
			return
		}
		var in store.PromptResponseInput
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.AddPromptResponse(r.Context(), club, p[2], uid, in)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
	case len(p) == 2 && p[1] == "polls" && r.Method == http.MethodPost:
		var in store.PollInput
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.CreatePoll(r.Context(), club, uid, in)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
	case len(p) == 4 && p[1] == "polls" && p[3] == "vote" && r.Method == http.MethodPut:
		if _, e := uuid.Parse(p[2]); e != nil {
			notFound(w)
			return
		}
		var in struct {
			OptionIndex int `json:"optionIndex"`
		}
		if !decode(w, r, &in) {
			return
		}
		respond(w, nil, s.store.VotePoll(r.Context(), club, p[2], uid, in.OptionIndex))
	case len(p) == 4 && p[1] == "polls" && p[3] == "close" && r.Method == http.MethodPut:
		if _, e := uuid.Parse(p[2]); e != nil {
			notFound(w)
			return
		}
		respond(w, nil, s.store.ClosePoll(r.Context(), club, p[2], uid))
	default:
		notFound(w)
	}
}
