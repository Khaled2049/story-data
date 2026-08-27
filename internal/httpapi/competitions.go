package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

// sweepDuePhases applies any clock-driven phase transition before the request
// reads or acts on a competition, standing in for the scheduled sweep the
// service never had. Both competition handlers call it, which covers every
// route: a stale `scheduled` is what made SubmitCompetition refuse entries.
//
// A failure is logged and swallowed rather than failing the request. The sweep
// is maintenance the caller did not ask for, and the request's own query will
// surface a genuinely broken database a moment later; refusing to serve a read
// because a bookkeeping UPDATE hit a lock timeout trades a stale phase for an
// outage.
func (s *Server) sweepDuePhases(r *http.Request) {
	if e := s.store.SweepDuePhases(r.Context()); e != nil {
		slog.Error("competition phase sweep failed", "error", e)
	}
}

func (s *Server) competitions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/competitions" {
		notFound(w)
		return
	}
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	s.sweepDuePhases(r)
	page, e := s.store.ListCompetitions(r.Context(), s.optionalUser(r), r.URL.Query().Get("cursor"), limit)
	if e != nil {
		respond(w, nil, e)
		return
	}
	// The body stays a bare array — the frontend maps over it directly — so the
	// continuation token rides in a header rather than wrapping the payload in
	// an envelope that would break every existing client.
	if page.NextCursor != "" {
		w.Header().Set("X-Next-Cursor", page.NextCursor)
	}
	respond(w, page.Competitions, nil)
}

// pageLimit reads ?limit=, writing the 400 itself when it is not a positive
// integer. Absent means the store's default; too large is clamped there, not
// here, so a client asking for more than a page simply gets a page.
func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "limit must be a positive integer")
		return 0, false
	}
	return n, true
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
	// After the id is known to be well formed, so a scan for garbage paths
	// cannot drive writes.
	s.sweepDuePhases(r)
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
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	switch {
	case len(p) == 2 && p[1] == "submissions" && r.Method == http.MethodGet:
		// Moved inside the authentication gate: this used to be the one route
		// in the subtree an anonymous caller could reach, and it answers with
		// the uid of every entrant. The frontend already requires a signed-in
		// user to call it.
		x, e := s.store.ListSubmissions(r.Context(), id, uid)
		respond(w, x, e)
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
func (s *Server) adminTokenGrants(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.user(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.isAdmin(r) {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	var in struct {
		UserID         string `json:"userId"`
		Amount         string `json:"amount"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !decode(w, r, &in) {
		return
	}
	x, e := s.store.GrantTokens(r.Context(), in.UserID, in.Amount, in.IdempotencyKey)
	respond(w, x, e)
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
