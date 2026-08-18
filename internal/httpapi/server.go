package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kh1011/novelsync-story-data/internal/auth"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

type Server struct {
	store   *store.Store
	auth    *auth.Verifier
	origins map[string]bool
	limiter *limiter
	// viewSalt keys the hash that stands in for an anonymous reader's address
	// in the view-dedup table. Generated per process: the table must be able
	// to tell two readers apart today without holding anything that could
	// identify either of them later.
	viewSalt []byte
}

// New builds the router. rl bounds how fast one caller may hit the service; a
// zeroed RateLimit disables the middleware, which is what tests and local
// stacks want and no deployment should.
func New(s *store.Store, a *auth.Verifier, origins []string, rl RateLimit) http.Handler {
	x := &Server{store: s, auth: a, origins: make(map[string]bool, len(origins)), viewSalt: make([]byte, 32)}
	if _, err := rand.Read(x.viewSalt); err != nil {
		// A predictable salt would let anyone precompute the key for an
		// address and poison another reader's dedup row.
		panic("story-data: cannot read random bytes for the view salt: " + err.Error())
	}
	if !rl.Disabled() {
		x.limiter = newLimiter(rl)
	}
	for _, o := range origins {
		x.origins[o] = true
	}
	m := http.NewServeMux()
	m.HandleFunc("/health", x.health)
	m.HandleFunc("/v1/public/", x.public)
	m.HandleFunc("/v1/public/profiles", x.profiles)
	m.HandleFunc("/v1/public/profiles/", x.profiles)
	m.HandleFunc("/v1/profiles/me", x.myProfile)
	m.HandleFunc("/v1/me/reading-history", x.readingHistory)
	m.HandleFunc("/v1/me/reading-progress/", x.readingProgress)
	m.HandleFunc("/v1/public/guestbooks/", x.publicGuestbook)
	m.HandleFunc("/v1/guestbooks/", x.guestbook)
	m.HandleFunc("/v1/book-clubs", x.bookClubs)
	m.HandleFunc("/v1/book-clubs/", x.bookClub)
	m.HandleFunc("/v1/competitions", x.competitions)
	m.HandleFunc("/v1/competitions/", x.competition)
	m.HandleFunc("/v1/me/competitions/drafts", x.myCompetitions)
	m.HandleFunc("/v1/me/token-balance", x.myCompetitions)
	m.HandleFunc("/v1/me/token-faucet", x.myCompetitions)
	m.HandleFunc("/v1/admin/token-grants", x.adminTokenGrants)
	m.HandleFunc("/v1/competition-drafts", x.competitionDrafts)
	m.HandleFunc("/v1/competition-publish", x.competitionPublish)
	m.HandleFunc("/v1/me/follows", x.myFollows)
	m.HandleFunc("/v1/profiles/", x.profileAction)
	m.HandleFunc("/v1/stories", x.stories)
	m.HandleFunc("/v1/stories/", x.story)
	// Logging is outermost so that a request rejected by the rate limiter is
	// logged too — during an attack those rejections are the signal. Rate
	// limiting then sits inside CORS, so a throttled caller still gets the
	// headers their browser needs to read the 429, and outside everything
	// else, so a throttled request costs no database work.
	return x.withLogging(x.withCORS(x.withRateLimit(x.withJSON(m))))
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		allowed := o != "" && s.origins[o]
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Add("Vary", "Origin")
		}
		if allowed && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match")
			w.Header().Set("Access-Control-Max-Age", "3600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (s *Server) stories(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		x, e := s.store.ListStories(r.Context(), uid)
		respond(w, x, e)
	case http.MethodPost:
		var in store.StoryInput
		if !decode(w, r, &in) || !required(w, "title", in.Title) {
			return
		}
		x, e := s.store.CreateStory(r.Context(), uid, in)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
	default:
		method(w)
	}
}
func (s *Server) story(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/stories/"), "/"), "/")
	if len(p) == 0 || p[0] == "" {
		notFound(w)
		return
	}
	// Guards the whole /v1/stories/{id}/... subtree in one place.
	if !uuidPath(w, p[0]) {
		return
	}
	if len(p) == 1 {
		s.storyResource(w, r, uid, p[0])
		return
	}
	if p[1] == "context" && len(p) == 2 && r.Method == http.MethodGet {
		x, e := s.store.StoryContext(r.Context(), p[0], uid)
		respond(w, x, e)
		return
	}
	if p[1] == "social" && len(p) == 3 && p[2] == "me" && r.Method == http.MethodGet {
		x, e := s.store.StorySocialMe(r.Context(), p[0], uid)
		respond(w, x, e)
		return
	}
	if p[1] == "likes" && len(p) == 3 && p[2] == "me" {
		s.storyLike(w, r, uid, p[0])
		return
	}
	if p[1] == "ratings" && len(p) == 2 && r.Method == http.MethodPost {
		var in struct {
			Rating int `json:"rating"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.CreateStoryRating(r.Context(), p[0], uid, in.Rating)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
		return
	}
	if p[1] == "characters" || p[1] == "places" || p[1] == "plots" {
		s.worldbuilding(w, r, uid, p[0], p)
		return
	}
	if p[1] == "chapters" && len(p) >= 4 && p[3] == "comments" {
		// Guarded here rather than inside `comments`: this branch returns
		// before the uuidPath checks further down, and every route below puts
		// these segments straight into a query against a uuid column.
		if !uuidPath(w, p[2]) {
			return
		}
		if len(p) >= 5 && !uuidPath(w, p[4]) {
			return
		}
		s.comments(w, r, uid, p[0], p)
		return
	}
	if p[1] != "chapters" {
		notFound(w)
		return
	}
	if len(p) == 2 {
		s.chapters(w, r, uid, p[0])
		return
	}
	if len(p) == 3 {
		if !uuidPath(w, p[2]) {
			return
		}
		s.chapter(w, r, uid, p[0], p[2])
		return
	}
	if len(p) == 4 && p[1] == "chapters" && p[3] == "summary" && r.Method == http.MethodPut {
		if !uuidPath(w, p[2]) {
			return
		}
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		var in store.ChapterSummaryInput
		if !decode(w, r, &in) || !required(w, "summary", in.Summary) {
			return
		}
		x, e := s.store.PutChapterSummary(r.Context(), p[0], p[2], uid, rev, in)
		respond(w, x, e)
		return
	}
	notFound(w)
}
func (s *Server) storyLike(w http.ResponseWriter, r *http.Request, uid, storyID string) {
	var liked bool
	switch r.Method {
	case http.MethodPut:
		liked = true
	case http.MethodDelete:
		liked = false
	default:
		method(w)
		return
	}
	x, e := s.store.SetStoryLike(r.Context(), storyID, uid, liked)
	respond(w, x, e)
}
func (s *Server) comments(w http.ResponseWriter, r *http.Request, uid, storyID string, p []string) {
	chapterID := p[2]
	if len(p) == 4 {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		var in store.CommentInput
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.CreateComment(r.Context(), storyID, chapterID, uid, in)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
		return
	}
	if len(p) == 6 && p[5] == "likes" {
		if r.Method != http.MethodPut && r.Method != http.MethodDelete {
			method(w)
			return
		}
		x, e := s.store.SetCommentLike(r.Context(), storyID, chapterID, p[4], uid, r.Method == http.MethodPut)
		respond(w, x, e)
		return
	}
	if len(p) != 5 {
		notFound(w)
		return
	}
	commentID := p[4]
	switch r.Method {
	case http.MethodPatch:
		var in struct {
			Message string `json:"message"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.UpdateComment(r.Context(), storyID, chapterID, commentID, uid, in.Message)
		respond(w, x, e)
	case http.MethodDelete:
		respond(w, nil, s.store.DeleteComment(r.Context(), storyID, chapterID, commentID, uid))
	default:
		method(w)
	}
}
func (s *Server) storyResource(w http.ResponseWriter, r *http.Request, uid, id string) {
	switch r.Method {
	case http.MethodGet:
		x, e := s.store.GetStory(r.Context(), id, uid)
		respond(w, x, e)
	case http.MethodPatch:
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		var in store.StoryInput
		if !decode(w, r, &in) || !required(w, "title", in.Title) {
			return
		}
		x, e := s.store.UpdateStory(r.Context(), id, uid, rev, in)
		respond(w, x, e)
	case http.MethodDelete:
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		respond(w, nil, s.store.DeleteStory(r.Context(), id, uid, rev))
	default:
		method(w)
	}
}
func (s *Server) chapters(w http.ResponseWriter, r *http.Request, uid, storyID string) {
	switch r.Method {
	case http.MethodGet:
		// ?content=false returns the running order without chapter bodies.
		if r.URL.Query().Get("content") == "false" {
			x, e := s.store.ListChapterIndex(r.Context(), storyID, uid)
			respond(w, x, e)
			return
		}
		x, e := s.store.ListChapters(r.Context(), storyID, uid)
		respond(w, x, e)
	case http.MethodPost:
		var in store.ChapterInput
		if !decode(w, r, &in) || !required(w, "title", in.Title) {
			return
		}
		x, e := s.store.CreateChapter(r.Context(), storyID, uid, in)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
	default:
		method(w)
	}
}
func (s *Server) chapter(w http.ResponseWriter, r *http.Request, uid, storyID, id string) {
	switch r.Method {
	case http.MethodGet:
		x, e := s.store.GetChapter(r.Context(), storyID, id, uid)
		respond(w, x, e)
	case http.MethodPatch:
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		var in store.ChapterInput
		if !decode(w, r, &in) || !required(w, "title", in.Title) {
			return
		}
		x, e := s.store.UpdateChapter(r.Context(), storyID, id, uid, rev, in)
		respond(w, x, e)
	case http.MethodDelete:
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		respond(w, nil, s.store.DeleteChapter(r.Context(), storyID, id, uid, rev))
	default:
		method(w)
	}
}
func (s *Server) user(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid, e := s.auth.UserID(r.Context(), r)
	if e != nil {
		writeError(w, http.StatusUnauthorized, e.Error())
		return "", false
	}
	logUser(w, uid)
	return uid, true
}
func (s *Server) withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, d any) bool {
	de := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	de.DisallowUnknownFields()
	if e := de.Decode(d); e != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+e.Error())
		return false
	}
	return true
}
func revision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	x, e := strconv.ParseInt(strings.TrimSpace(r.Header.Get("If-Match")), 10, 64)
	if e != nil || x < 1 {
		writeError(w, http.StatusPreconditionRequired, "If-Match must be a positive revision")
		return 0, false
	}
	return x, true
}
func respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		if v == nil {
			w.WriteHeader(http.StatusNoContent)
		} else {
			write(w, http.StatusOK, v)
		}
		return
	}
	switch {
	case errors.Is(e, store.ErrNotFound):
		writeError(w, http.StatusNotFound, e.Error())
	case errors.Is(e, store.ErrForbidden):
		writeError(w, http.StatusForbidden, e.Error())
	case errors.Is(e, store.ErrConflict), errors.Is(e, store.ErrUsernameTaken):
		writeError(w, http.StatusConflict, e.Error())
	case errors.Is(e, store.ErrLimit):
		writeError(w, http.StatusUnprocessableEntity, e.Error())
	case errors.Is(e, store.ErrRateLimit):
		writeError(w, http.StatusTooManyRequests, e.Error())
	case errors.Is(e, store.ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, e.Error())
	default:
		if status, msg, ok := translatePgError(e); ok {
			writeError(w, status, msg)
			return
		}
		// The client message stays generic; the detail goes to the log, which
		// is the only place it belongs.
		logError(w, e)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// translatePgError turns the constraint violations that a bad request can
// provoke into the 4xx they are. A validator in internal/store should catch
// each of these first — this is the backstop for the one that gets missed, and
// it matters because an attacker-triggerable 500 poisons the error rate that
// is the natural alert for a real outage.
func translatePgError(e error) (int, string, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(e, &pgErr) {
		return 0, "", false
	}
	switch pgErr.Code {
	case "23514", "23502", "22P02", "22001", "22003":
		// check_violation, not_null_violation, invalid_text_representation,
		// string_data_right_truncation, numeric_value_out_of_range.
		return http.StatusUnprocessableEntity, "invalid input", true
	case "23503":
		// foreign_key_violation: the row this one points at is not there.
		return http.StatusNotFound, "not found", true
	case "23505":
		// unique_violation that no store method claimed as its own conflict.
		return http.StatusConflict, "already exists", true
	}
	return 0, "", false
}
func write(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// required rejects a blank mandatory field. It writes the 400 itself, because
// the callers that guard on it return immediately — an earlier version of this
// check just returned, which let the handler answer 200 with an empty body.
func required(w http.ResponseWriter, field, value string) bool {
	if strings.TrimSpace(value) == "" {
		writeError(w, http.StatusBadRequest, field+" is required")
		return false
	}
	return true
}

// uuidPath reports whether a path segment is a well-formed UUID. Passing a
// malformed one straight into a query against a uuid column makes PostgreSQL
// raise, which surfaced as a 500 for what is really a bad URL.
func uuidPath(w http.ResponseWriter, id string) bool {
	if _, err := uuid.Parse(id); err != nil {
		notFound(w)
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	write(w, status, map[string]string{"error": msg})
}
func method(w http.ResponseWriter)   { writeError(w, http.StatusMethodNotAllowed, "method not allowed") }
func notFound(w http.ResponseWriter) { writeError(w, http.StatusNotFound, "not found") }
