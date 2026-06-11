package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// ListCommandsHandler: GET /api/commands?limit=N&before=ID
//
// Returns metadata only (no output bodies). The empty list is rendered as
// `[]`, not `null` — frontends rely on the array type.
func ListCommandsHandler(s *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		before := r.URL.Query().Get("before")
		list, err := s.List(limit, before)
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

// fullRecord is the GetCommand response shape. It embeds the stored metadata
// and adds the `output` field read from the log file.
type fullRecord struct {
	store.Record
	Output string `json:"output"`
}

// GetCommandHandler: GET /api/commands/{id}
//
// Returns full record + reads output file contents into a transient field.
// If the command is still running the output file may not exist yet; in
// that case `output` is empty (the live stream is delivered separately over
// the WebSocket).
func GetCommandHandler(s *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		rec, err := s.Get(id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such command")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		out, err := s.ReadOutput(id)
		if err != nil {
			// Output file truly broken (not just absent). Surface as 500.
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fullRecord{Record: rec, Output: string(out)})
	})
}

// StopCommandHandler: POST /api/commands/{id}/stop
//
// Returns 204 if the named command is the currently running one and SIGKILL
// was sent. Returns 409 otherwise (command isn't running, or a different
// command is).
func StopCommandHandler(sh *shell.Shell) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		cur := sh.CurrentCommand()
		if cur == nil || cur.ID != id {
			writeError(w, http.StatusConflict, "not_running", "command is not currently running")
			return
		}
		sh.Stop()
		w.WriteHeader(http.StatusNoContent)
	})
}
