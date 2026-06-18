package claudestate

import (
	"context"
	"sync"
)

// SessionState holds one Alfred session's in-memory Claude state plus
// its Persister. All mutations route through Apply (added in Task 5);
// callers wanting a read-only view call View under the read lock.
//
// Concurrency model: a single sync.RWMutex protects `state`. Apply
// takes the write lock for the whole reducer run; View takes the
// read lock for the duration of its callback. Callers SHOULD copy
// state inside View and do expensive work (JSON marshal, HTTP write)
// outside the lock.
type SessionState struct {
	sessionID  string
	claudeUUID string

	mu    sync.RWMutex
	state ClaudeState

	persister *Persister // nil until AttachPersister is called
}

// NewSessionState returns a fresh SessionState with an empty
// ClaudeState. The Persister is not attached — the SessionManager
// (Plan 2) wires it after construction so tests can run state logic
// without touching disk.
func NewSessionState(sessionID, claudeUUID string) *SessionState {
	return &SessionState{
		sessionID:  sessionID,
		claudeUUID: claudeUUID,
		state:      EmptyClaudeState(),
	}
}

// SessionID returns the Alfred session id this state belongs to.
func (s *SessionState) SessionID() string { return s.sessionID }

// ClaudeUUID returns the current Claude CLI session uuid (mutable
// after a /compact rotation; updated via SetClaudeUUID).
func (s *SessionState) ClaudeUUID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claudeUUID
}

// SetClaudeUUID updates the tracked Claude uuid. Called after the
// runner reports a rotation.
func (s *SessionState) SetClaudeUUID(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claudeUUID = uuid
}

// View runs fn against the state under the read lock. fn MUST NOT
// retain a reference to the *ClaudeState after returning; copy out
// what is needed and let the lock release.
func (s *SessionState) View(fn func(*ClaudeState)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.state)
}

// Close flushes the Persister (if attached) and stops its goroutine.
// Safe to call when persister is nil (test path).
func (s *SessionState) Close(ctx context.Context) error {
	if s.persister == nil {
		return nil
	}
	return s.persister.Close(ctx)
}
