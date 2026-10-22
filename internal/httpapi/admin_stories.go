package httpapi

import (
	"net/http"
	"strings"
)

// Moderation has its own routes: ordinary story reads remain owner-scoped,
// including for admins who are using the public reader.
func (s *Server) adminStories(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.admin(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/v1/admin/stories" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		limit, ok := pageLimit(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		page, err := s.store.ListAdminStories(r.Context(), true, q.Get("q"), q.Get("status"), q.Get("cursor"), limit)
		respond(w, page, err)
		return
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/admin/stories/"), "/")
	if !uuidPath(w, p[0]) {
		return
	}
	if len(p) == 1 {
		switch r.Method {
		case http.MethodGet:
			story, err := s.store.GetAdminStory(r.Context(), true, uid, p[0])
			respond(w, story, err)
		case http.MethodDelete:
			rev, ok := revision(w, r)
			if !ok {
				return
			}
			respond(w, nil, s.store.DeleteAdminStory(r.Context(), true, uid, p[0], rev))
		default:
			method(w)
		}
		return
	}
	if (len(p) == 2 || len(p) == 3) && p[1] == "chapters" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		if len(p) == 2 {
			chapters, err := s.store.ListAdminChapterIndex(r.Context(), true, p[0])
			respond(w, chapters, err)
		} else {
			if !uuidPath(w, p[2]) {
				return
			}
			chapter, err := s.store.GetAdminChapter(r.Context(), true, uid, p[0], p[2])
			respond(w, chapter, err)
		}
		return
	}
	notFound(w)
}
