package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// ListCommandsHandler: GET /api/sessions/{sid}/commands?limit=N&before=ID
//
// Returns metadata only (no output bodies). Empty list is `[]`. An
// unknown session returns `[]` rather than 404 — the frontend treats
// "no session" and "session with no commands" the same; this is also
// kinder to race conditions during cross-tab deletes.
func ListCommandsHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		before := r.URL.Query().Get("before")
		list, err := m.StoreFor().List(sid, limit, before)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if list == nil {
			list = []store.Record{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})
}

type fullRecord struct {
	store.Record
	Output string `json:"output"`
}

// GetCommandHandler: GET /api/sessions/{sid}/commands/{id}
func GetCommandHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		id := chi.URLParam(r, "id")
		rec, err := m.StoreFor().Get(sid, id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such command")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		out, err := m.StoreFor().ReadOutput(sid, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fullRecord{Record: rec, Output: string(out)})
	})
}

// StopCommandHandler: POST /api/sessions/{sid}/commands/{id}/stop
// Returns 204 if id is currently running in sid; 409 otherwise;
// 404 if sid is unknown.
func StopCommandHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		id := chi.URLParam(r, "id")
		sh, err := m.Get(sid)
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "manager_error", err.Error())
			return
		}
		cur := sh.CurrentCommand()
		if cur == nil || cur.ID != id {
			writeError(w, http.StatusConflict, "not_running", "command is not currently running")
			return
		}
		// Stamp status=stopped BEFORE issuing the SIGKILL so the Ended
		// event handler in ws.go sees it and does not promote to
		// "completed". This is the only place that writes StatusStopped.
		if rec, err := m.StoreFor().Get(sid, id); err == nil {
			rec.Status = store.StatusStopped
			_ = m.StoreFor().Save(sid, rec)
		}
		sh.Stop()
		w.WriteHeader(http.StatusNoContent)
	})
}
