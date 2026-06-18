package api

import (
	"github.com/jesseliu/headless-alfred/internal/claudehistory"
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

// JsonlLocatorAdapter wraps claudehistory.Locator to satisfy the
// claudestate.JsonlLocator interface. Trivial passthrough — they
// already share the same Locate(sessionID, uuid) signature.
type JsonlLocatorAdapter struct {
	inner *claudehistory.Locator
}

// NewJsonlLocatorAdapter wires the adapter. Panics on a nil locator
// (programmer error at the boundary).
func NewJsonlLocatorAdapter(inner *claudehistory.Locator) *JsonlLocatorAdapter {
	if inner == nil {
		panic("api.NewJsonlLocatorAdapter: nil locator")
	}
	return &JsonlLocatorAdapter{inner: inner}
}

// Locate delegates to the upstream claudehistory.Locator using both
// sessionID (cache key) and claudeUUID (file-name component).
func (a *JsonlLocatorAdapter) Locate(sessionID, claudeUUID string) (string, error) {
	return a.inner.Locate(sessionID, claudeUUID)
}
