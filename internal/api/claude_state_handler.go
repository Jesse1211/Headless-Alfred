package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudestate"
)

// MetaResolver translates an Alfred session id into the current
// Claude CLI session uuid. Implemented in Task 4 by adapting the
// existing session.Manager.
type MetaResolver interface {
	ClaudeUUIDFor(sessionID string) (string, error)
}

// ErrUnknownSession is what MetaResolver returns when the session
// doesn't exist. Maps to HTTP 404.
var ErrUnknownSession = errors.New("api: unknown session")

// GetClaudeStateHandler serves the full ClaudeState for a session.
// Replaces /claude-history; the old endpoint is kept one release
// cycle with Deprecation headers (Task 5).
func GetClaudeStateHandler(mgr *claudestate.SessionManager, meta MetaResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if sid == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "missing sid")
			return
		}
		uuid, err := meta.ClaudeUUIDFor(sid)
		if errors.Is(err, ErrUnknownSession) {
			writeError(w, http.StatusNotFound, "unknown_session", "no such session")
			return
		}
		if err != nil {
			slog.Error("claude-state: meta resolve", "sid", sid, "err", err)
			writeError(w, http.StatusInternalServerError, "meta_error", "resolve failed")
			return
		}
		st, err := mgr.GetOrLoad(sid, uuid)
		if err != nil {
			slog.Error("claude-state: load", "sid", sid, "err", err)
			writeError(w, http.StatusInternalServerError, "load_failed", "load failed")
			return
		}
		var snap claudestate.ClaudeState
		st.View(func(s *claudestate.ClaudeState) {
			snap = s.DeepCopy()
		})
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(snap); err != nil {
			slog.Error("claude-state: encode", "sid", sid, "err", err)
		}
	})
}
