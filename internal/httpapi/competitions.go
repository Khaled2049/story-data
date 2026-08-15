package httpapi

import (
	"github.com/google/uuid"
	"github.com/kh1011/novelsync-story-data/internal/store"
	"net/http"
	"strings"
)

func (s *Server) competitions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/competitions" {
		notFound(w)
		return
	}
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	x, e := s.store.ListCompetitions(r.Context(), s.optionalUser(r))
	respond(w, x, e)
}
func (s *Server) competition(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/competitions/"), "/"), "/")
	if len(p) == 0 || p[0] == "" {
		notFound(w)
		return
	}
	if _, e := uuid.Parse(p[0]); e != nil {
		notFound(w)
		return
	}
	id := p[0]
	if len(p) == 1 {
		if r.Method == http.MethodGet {
			x, e := s.store.GetCompetition(r.Context(), id, s.optionalUser(r))
			respond(w, x, e)
			return
		}
		uid, ok := s.user(w, r)
		if !ok {
			return
		}
		if r.Method == http.MethodDelete {
			respond(w, nil, s.store.DiscardCompetitionDraft(r.Context(), id, uid))
			return
		}
		if r.Method == http.MethodPatch {
			var in store.CompetitionInput
			if !decode(w, r, &in) {
				return
			}
			x, e := s.store.UpdateCompetition(r.Context(), id, uid, in)
			respond(w, x, e)
			return
		}
		method(w)
		return
	}
	if len(p) == 2 && p[1] == "submissions" && r.Method == http.MethodGet {
		x, e := s.store.ListSubmissions(r.Context(), id)
		respond(w, x, e)
		return
	}
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	switch {
	case len(p) == 2 && p[1] == "join" && r.Method == http.MethodPut:
		respond(w, nil, s.store.JoinCompetition(r.Context(), id, uid))
	case len(p) == 3 && p[1] == "submissions" && p[2] == "me" && r.Method == http.MethodPost:
		var in struct {
			StoryID string `json:"storyId"`
		}
		if !decode(w, r, &in) {
			return
		}
		respond(w, nil, s.store.SubmitCompetition(r.Context(), id, uid, in.StoryID))
	case len(p) == 3 && p[1] == "submissions" && p[2] == "me" && r.Method == http.MethodDelete:
		respond(w, nil, s.store.WithdrawCompetitionSubmission(r.Context(), id, uid))
	case len(p) == 3 && p[1] == "ballots" && p[2] == "me":
		if r.Method == http.MethodGet {
			x, e := s.store.MyBallot(r.Context(), id, uid)
			respond(w, x, e)
		} else if r.Method == http.MethodPut {
			var in struct {
				SubmissionIDs []string `json:"submissionIds"`
			}
			if !decode(w, r, &in) {
				return
			}
			respond(w, nil, s.store.CastBallot(r.Context(), id, uid, in.SubmissionIDs))
		} else {
			method(w)
		}
	case len(p) == 2 && p[1] == "settle" && r.Method == http.MethodPost:
		admin := s.isAdmin(r)
		x, e := s.store.SettleCompetition(r.Context(), id, uid, admin)
		respond(w, x, e)
	case len(p) == 2 && p[1] == "cancel" && r.Method == http.MethodPost:
		var in struct {
			Reason string `json:"reason"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.CancelCompetition(r.Context(), id, uid, s.isAdmin(r), in.Reason)
		respond(w, x, e)
	case len(p) == 2 && p[1] == "advance" && r.Method == http.MethodPost:
		var in struct {
			TargetPhase string `json:"targetPhase"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.AdvanceCompetition(r.Context(), id, uid, in.TargetPhase, s.isAdmin(r))
		respond(w, x, e)
	default:
		notFound(w)
	}
}
func (s *Server) myCompetitions(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	if r.URL.Path == "/v1/me/competitions/drafts" && r.Method == http.MethodGet {
		x, e := s.store.ListDrafts(r.Context(), uid)
		respond(w, x, e)
		return
	}
	if r.URL.Path == "/v1/me/token-balance" && r.Method == http.MethodGet {
		x, e := s.store.TokenBalance(r.Context(), uid)
		respond(w, x, e)
		return
	}
	if r.URL.Path == "/v1/me/token-faucet" && r.Method == http.MethodPost {
		x, e := s.store.ClaimFaucet(r.Context(), uid)
		respond(w, x, e)
		return
	}
	notFound(w)
}
func (s *Server) competitionDrafts(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in store.CompetitionInput
	if !decode(w, r, &in) {
		return
	}
	id := r.URL.Query().Get("competitionId")
	x, e := s.store.SaveDraft(r.Context(), uid, id, in)
	if e == nil {
		write(w, http.StatusCreated, x)
	} else {
		respond(w, nil, e)
	}
}
func (s *Server) competitionPublish(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	var in struct {
		CompetitionID string `json:"competitionId"`
	}
	if r.Method != http.MethodPost || !decode(w, r, &in) {
		return
	}
	x, e := s.store.PublishCompetition(r.Context(), in.CompetitionID, uid)
	respond(w, x, e)
}
func (s *Server) isAdmin(r *http.Request) bool {
	ok, e := s.auth.IsAdmin(r.Context(), r)
	return e == nil && ok
}
