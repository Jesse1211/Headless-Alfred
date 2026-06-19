package api

import (
	"path/filepath"

	"github.com/jesseliu/headless-alfred/internal/session"
)

// SessionMetaResolver adapts session.Manager into the MetaResolver
// interface the claude-state handler depends on. Returns
// ErrUnknownSession when the session isn't present so the handler
// can map it to 404.
type SessionMetaResolver struct {
	mgr *session.Manager
}

// NewSessionMetaResolver wires the adapter. Panics on a nil manager
// (programmer error at the boundary).
func NewSessionMetaResolver(m *session.Manager) *SessionMetaResolver {
	if m == nil {
		panic("api.NewSessionMetaResolver: nil manager")
	}
	return &SessionMetaResolver{mgr: m}
}

// ClaudeUUIDFor returns the Claude conversation uuid currently stored
// on the Alfred session, or ErrUnknownSession when the id is unknown.
// An empty uuid is valid — it just means the session hasn't entered
// Claude mode yet; the loader treats that as "no jsonl available."
func (r *SessionMetaResolver) ClaudeUUIDFor(sessionID string) (string, error) {
	_, ok := r.mgr.FindByID(sessionID)
	if !ok {
		return "", ErrUnknownSession
	}
	return r.mgr.GetClaudeSessionID(sessionID), nil
}

// SessionCWDResolver adapts session.Manager into the CWDResolver interface
// the bg-task log handler depends on. It mirrors the cwd resolution logic in
// handleClaudePrompt: Get the shell's pane_current_path via CurrentCWD, then
// resolve symlinks (ADR-008) so the path matches what the Claude CLI sees when
// computing the transcript directory hash.
type SessionCWDResolver struct {
	mgr *session.Manager
}

// NewSessionCWDResolver wires the adapter. Panics on a nil manager.
func NewSessionCWDResolver(m *session.Manager) *SessionCWDResolver {
	if m == nil {
		panic("api.NewSessionCWDResolver: nil manager")
	}
	return &SessionCWDResolver{mgr: m}
}

// CWDFor returns the realpath-cwd for the session's tmux pane, or
// ErrCWDUnknown when the shell is unavailable or returns an empty path.
func (r *SessionCWDResolver) CWDFor(sessionID string) (string, error) {
	sh, err := r.mgr.Get(sessionID)
	if err != nil {
		return "", ErrCWDUnknown
	}
	raw := sh.CurrentCWD()
	if raw == "" {
		return "", ErrCWDUnknown
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		// Symlink resolution failed (path doesn't exist yet); return raw.
		return raw, nil
	}
	return resolved, nil
}

