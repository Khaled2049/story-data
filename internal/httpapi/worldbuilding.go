package httpapi

import (
	"net/http"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

func (s *Server) worldbuilding(w http.ResponseWriter, r *http.Request, uid, storyID string, p []string) {
	if len(p) < 2 {
		notFound(w)
		return
	}
	switch p[1] {
	case "characters":
		s.characters(w, r, uid, storyID, p[2:])
	case "places":
		s.places(w, r, uid, storyID, p[2:])
	case "plots":
		s.plots(w, r, uid, storyID, p[2:])
	default:
		notFound(w)
	}
}
func (s *Server) characters(w http.ResponseWriter, r *http.Request, uid, sid string, p []string) {
	if len(p) == 0 {
		switch r.Method {
		case http.MethodGet:
			x, e := s.store.ListCharacters(r.Context(), sid, uid)
			respond(w, x, e)
		case http.MethodPost:
			var in store.CharacterInput
			if !decode(w, r, &in) || !required(w, "name", in.Name) {
				return
			}
			x, e := s.store.CreateCharacter(r.Context(), sid, uid, in)
			if e == nil {
				write(w, http.StatusCreated, x)
			} else {
				respond(w, nil, e)
			}
		default:
			method(w)
		}
		return
	}
	if len(p) != 1 {
		notFound(w)
		return
	}
	if !uuidPath(w, p[0]) {
		return
	}
	// Ahead of revision(): a read has no If-Match to supply.
	if r.Method == http.MethodGet {
		x, e := s.store.Character(r.Context(), sid, p[0], uid)
		respond(w, x, e)
		return
	}
	rev, ok := revision(w, r)
	if !ok {
		return
	}
	var e error
	switch r.Method {
	case http.MethodPatch:
		var in store.CharacterInput
		if !decode(w, r, &in) || !required(w, "name", in.Name) {
			return
		}
		x, e := s.store.UpdateCharacter(r.Context(), sid, p[0], uid, rev, in)
		respond(w, x, e)
	case http.MethodDelete:
		e = s.store.DeleteCharacter(r.Context(), sid, p[0], uid, rev)
		respond(w, nil, e)
	default:
		method(w)
	}
}
func (s *Server) places(w http.ResponseWriter, r *http.Request, uid, sid string, p []string) {
	if len(p) == 0 {
		switch r.Method {
		case http.MethodGet:
			x, e := s.store.ListPlaces(r.Context(), sid, uid)
			respond(w, x, e)
		case http.MethodPost:
			var in store.PlaceInput
			if !decode(w, r, &in) || !required(w, "name", in.Name) {
				return
			}
			x, e := s.store.CreatePlace(r.Context(), sid, uid, in)
			if e == nil {
				write(w, http.StatusCreated, x)
			} else {
				respond(w, nil, e)
			}
		default:
			method(w)
		}
		return
	}
	if len(p) != 1 {
		notFound(w)
		return
	}
	if !uuidPath(w, p[0]) {
		return
	}
	// Ahead of revision(): a read has no If-Match to supply.
	if r.Method == http.MethodGet {
		x, e := s.store.Place(r.Context(), sid, p[0], uid)
		respond(w, x, e)
		return
	}
	rev, ok := revision(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var in store.PlaceInput
		if !decode(w, r, &in) || !required(w, "name", in.Name) {
			return
		}
		x, e := s.store.UpdatePlace(r.Context(), sid, p[0], uid, rev, in)
		respond(w, x, e)
	case http.MethodDelete:
		respond(w, nil, s.store.DeletePlace(r.Context(), sid, p[0], uid, rev))
	default:
		method(w)
	}
}
func (s *Server) plots(w http.ResponseWriter, r *http.Request, uid, sid string, p []string) {
	if len(p) == 0 {
		switch r.Method {
		case http.MethodGet:
			x, e := s.store.ListPlots(r.Context(), sid, uid)
			respond(w, x, e)
		case http.MethodPost:
			var in store.PlotLineInput
			if !decode(w, r, &in) || !required(w, "name", in.Name) {
				return
			}
			x, e := s.store.CreatePlot(r.Context(), sid, uid, in)
			if e == nil {
				write(w, http.StatusCreated, x)
			} else {
				respond(w, nil, e)
			}
		default:
			method(w)
		}
		return
	}
	line := p[0]
	if !uuidPath(w, line) {
		return
	}
	if len(p) == 1 {
		// Ahead of revision(): a read has no If-Match to supply.
		if r.Method == http.MethodGet {
			x, e := s.store.Plot(r.Context(), sid, line, uid)
			respond(w, x, e)
			return
		}
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var in store.PlotLineInput
			if !decode(w, r, &in) || !required(w, "name", in.Name) {
				return
			}
			x, e := s.store.UpdatePlot(r.Context(), sid, line, uid, rev, in)
			respond(w, x, e)
		case http.MethodDelete:
			respond(w, nil, s.store.DeletePlot(r.Context(), sid, line, uid, rev))
		default:
			method(w)
		}
		return
	}
	if p[1] != "events" {
		notFound(w)
		return
	}
	s.plotEvents(w, r, uid, sid, line, p[2:])
}
func (s *Server) plotEvents(w http.ResponseWriter, r *http.Request, uid, sid, line string, p []string) {
	if len(p) == 1 && p[0] == "reorder" && r.Method == http.MethodPost {
		rev, ok := revision(w, r)
		if !ok {
			return
		}
		var in struct {
			OrderedIDs []string `json:"orderedIds"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.ReorderEvents(r.Context(), sid, line, uid, rev, in.OrderedIDs)
		respond(w, x, e)
		return
	}
	if len(p) == 0 && r.Method == http.MethodPost {
		var in store.PlotEventInput
		if !decode(w, r, &in) || !required(w, "name", in.Name) {
			return
		}
		x, e := s.store.CreateEvent(r.Context(), sid, line, uid, in)
		if e == nil {
			write(w, http.StatusCreated, x)
		} else {
			respond(w, nil, e)
		}
		return
	}
	if len(p) != 1 {
		notFound(w)
		return
	}
	if !uuidPath(w, p[0]) {
		return
	}
	rev, ok := revision(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var in store.PlotEventInput
		if !decode(w, r, &in) || !required(w, "name", in.Name) {
			return
		}
		x, e := s.store.UpdateEvent(r.Context(), sid, line, p[0], uid, rev, in)
		respond(w, x, e)
	case http.MethodDelete:
		x, e := s.store.DeleteEvent(r.Context(), sid, line, p[0], uid, rev)
		respond(w, x, e)
	default:
		method(w)
	}
}
