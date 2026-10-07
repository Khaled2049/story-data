package httpapi

import (
	"net/http"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

func (s *Server) writerAgreement(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.user(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		x, e := s.store.GetWriterAgreement(r.Context(), uid)
		respond(w, x, e)
	case http.MethodPost:
		// Delegated service identities (including MCP tools) must never attest
		// to a human's age, content rights, or agreement to contract terms.
		if r.Header.Get("X-Service-Token") != "" {
			respond(w, nil, store.ErrForbidden)
			return
		}
		var in store.WriterAgreementInput
		if !decode(w, r, &in) {
			return
		}
		x, e := s.store.AcceptWriterAgreement(r.Context(), uid, in)
		respond(w, x, e)
	default:
		method(w)
	}
}
