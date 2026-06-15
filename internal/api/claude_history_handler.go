package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudehistory"
)

// claudeSessionIDLookup is the subset of *session.Manager the handler
// needs. Lets tests stub it without spinning up a real manager.
type claudeSessionIDLookup interface {
	GetClaudeSessionID(sessionID string) string
}

// GetClaudeHistoryHandler serves the reconstructed Claude UI chat
// history for a session by reading the underlying CLI jsonl file.
//
// 200 + [] for: session never entered Claude (no ClaudeSessionID), or
// jsonl file not found on disk. 200 + turns for normal case. 500 only
// for unexpected I/O explosions. There is no 404 for "no history" —
// empty is a valid state for any session.
func GetClaudeHistoryHandler(lookup claudeSessionIDLookup, locator *claudehistory.Locator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")

		// Clamp limit to [1, 500]; default 100.
		limit := 100
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				limit = n
			}
		}
		if limit < 1 {
			limit = 1
		}
		if limit > 500 {
			limit = 500
		}
		before := r.URL.Query().Get("before")

		uuid := lookup.GetClaudeSessionID(sid)
		if uuid == "" {
			writeJSON(w, http.StatusOK, []claudehistory.Turn{})
			return
		}

		path, err := locator.Locate(sid, uuid)
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("claude history jsonl missing", "sid", sid, "uuid", uuid)
			writeJSON(w, http.StatusOK, []claudehistory.Turn{})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "history_error", err.Error())
			return
		}

		turns, err := claudehistory.Parse(path, limit, before)
		if err != nil {
			// Locate succeeded so the file exists. Parse-side failures
			// are already logged + best-effort partial inside Parse,
			// so this branch fires only on Open failure (file disappeared
			// between Locate and Parse) or a wholesale I/O error.
			slog.Warn("claudehistory.Parse failed", "sid", sid, "path", path, "err", err)
			writeError(w, http.StatusInternalServerError, "history_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, turns)
	})
}

// writeJSON is a tiny convenience to JSON-encode + set the content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
