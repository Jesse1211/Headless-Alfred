package api

import (
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

