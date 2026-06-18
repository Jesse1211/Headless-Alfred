package claudestate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// JsonlLocator hides the existing claudehistory.Locator behind a
// minimal interface so the manager doesn't pull every claudehistory
// detail into its tests. The production wiring (Plan 2 Task 4)
// adapts claudehistory.Locator into this interface.
type JsonlLocator interface {
	// Locate returns the jsonl file path for a Claude session.
	// sessionID is the Alfred session id (used by the upstream
	// claudehistory.Locator as a cache key); claudeUUID is the
	// Claude CLI's per-conversation uuid embedded in the file name.
	Locate(sessionID, claudeUUID string) (string, error)
}

// SessionManager is the process-wide registry of in-memory
// ClaudeState. Tracks one *SessionState per Alfred session id, lazily
// constructed on first GetOrLoad call. Shutdown flushes every
// attached Persister synchronously.
type SessionManager struct {
	dataDir string
	locator JsonlLocator

	mu       sync.RWMutex
	sessions map[string]*SessionState
	closed   bool

	loadGroup singleflight.Group

	persistDebounce time.Duration
}

// NewSessionManager constructs a manager rooted at dataDir. The
// locator is consulted lazily inside GetOrLoad; passing a nil
// locator panics (programmer error: caller forgot to wire it).
func NewSessionManager(dataDir string, locator JsonlLocator) *SessionManager {
	if locator == nil {
		panic("claudestate.NewSessionManager: nil locator")
	}
	return &SessionManager{
		dataDir:         dataDir,
		locator:         locator,
		sessions:        map[string]*SessionState{},
		persistDebounce: 100 * time.Millisecond,
	}
}

// ErrManagerClosed is returned by GetOrLoad after Shutdown.
var ErrManagerClosed = errors.New("claudestate: manager is closed")

// GetOrLoad returns the in-memory state for a session, constructing
// it from snapshot + jsonl on first access. Concurrent first-access
// callers share a single underlying load via singleflight.
func (m *SessionManager) GetOrLoad(sessionID, claudeUUID string) (*SessionState, error) {
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return nil, ErrManagerClosed
	}
	if s, ok := m.sessions[sessionID]; ok {
		m.mu.RUnlock()
		return s, nil
	}
	m.mu.RUnlock()

	v, err, _ := m.loadGroup.Do(sessionID, func() (any, error) {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, ErrManagerClosed
		}
		if s, ok := m.sessions[sessionID]; ok {
			m.mu.Unlock()
			return s, nil
		}
		m.mu.Unlock()

		s, err := m.buildSession(sessionID, claudeUUID)
		if err != nil {
			return nil, err
		}

		m.mu.Lock()
		// Double-check under write lock — another singleflight winner could
		// have raced ahead (shouldn't, but defensive). Drop ours if so.
		if existing, ok := m.sessions[sessionID]; ok {
			m.mu.Unlock()
			_ = s.Close(context.Background())
			return existing, nil
		}
		m.sessions[sessionID] = s
		m.mu.Unlock()
		return s, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*SessionState), nil
}

// buildSession resolves paths, runs Load, attaches a Persister, and
// starts the Persister's goroutine. Returns the wired SessionState.
func (m *SessionManager) buildSession(sessionID, claudeUUID string) (*SessionState, error) {
	snapPath := SnapshotPath(m.dataDir, sessionID)
	jsonlPath := ""
	if claudeUUID != "" {
		if p, err := m.locator.Locate(sessionID, claudeUUID); err == nil {
			jsonlPath = p
		}
	}
	state, err := Load(snapPath, jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("claudestate: load %s: %w", sessionID, err)
	}
	s := NewSessionState(sessionID, claudeUUID)
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()

	pers, err := NewPersister(snapPath, s, m.persistDebounce)
	if err != nil {
		return nil, fmt.Errorf("claudestate: persister %s: %w", sessionID, err)
	}
	s.AttachPersister(pers)
	go pers.Run(context.Background())
	return s, nil
}

// Snapshot returns the current ClaudeState for sessionID as a deep
// copy suitable for JSON serialization. The second return is false
// when the session has never been loaded; callers may choose to
// GetOrLoad first.
func (m *SessionManager) Snapshot(sessionID string) (ClaudeState, bool) {
	m.mu.RLock()
	s, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return ClaudeState{}, false
	}
	var out ClaudeState
	s.View(func(st *ClaudeState) {
		out = st.DeepCopy()
	})
	return out, true
}

// Shutdown closes every SessionState, flushing pending writes. Idempotent.
func (m *SessionManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*SessionState, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = nil
	m.mu.Unlock()

	var firstErr error
	for _, s := range sessions {
		if err := s.Close(ctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			slog.Error("claudestate: close session", "sessionID", s.SessionID(), "err", err)
		}
	}
	return firstErr
}
