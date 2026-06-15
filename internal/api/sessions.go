package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// ListSessionsHandler: GET /api/sessions
// Returns [] when empty (never null).
func ListSessionsHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := m.List(store.KindChat)
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
		// Empty body is OK (auto-name). ContentLength != 0 covers both
		// known-positive bodies and chunked transfers (ContentLength = -1)
		// where a proxy or HTTP/2 stream has stripped the header.
		if r.ContentLength != 0 {
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

// RenameSessionHandler: PATCH /api/sessions/{id}
// Body: { "name": string }
//
//	200 on success
//	404 not_found
//	422 bad_name (empty/over-length)
//	400 bad_request (malformed body)
func RenameSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
			return
		}
		err := m.Rename(id, req.Name)
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		case errors.Is(err, session.ErrBadName):
			writeError(w, http.StatusUnprocessableEntity, "bad_name", "session name is empty or too long")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "rename_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// DeleteSessionHandler: DELETE /api/sessions/{id}
//
//	204 on success
//	404 not_found
func DeleteSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		err := m.Close(id)
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
