package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// ListSessionsHandler: GET /api/sessions
// Returns [] when empty (never null).
func ListSessionsHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := m.List()
		if list == nil {
			list = []store.SessionMeta{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})
}

// CreateSessionHandler: POST /api/sessions
// Body: { "name"?: string } — missing/empty triggers auto-naming.
// Responses:
//
//	201 + SessionMeta on success
//	422 session_limit when MaxSessions is reached
//	422 bad_name on over-length name
//	400 bad_request on malformed JSON body
func CreateSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		// Empty body is OK (auto-name).
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
				return
			}
		}
		meta, err := m.Create(req.Name)
		switch {
		case errors.Is(err, session.ErrSessionLimit):
			writeError(w, http.StatusUnprocessableEntity, "session_limit", "session limit reached")
			return
		case errors.Is(err, session.ErrBadName):
			writeError(w, http.StatusUnprocessableEntity, "bad_name", "session name is empty or too long")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "create_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(meta)
	})
}
