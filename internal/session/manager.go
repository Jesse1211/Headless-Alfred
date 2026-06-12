package session

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// ErrSessionLimit is returned by Create when MaxSessions is exceeded.
var ErrSessionLimit = errors.New("session limit reached")

// ErrSessionNotFound is returned by Get, Rename, Close on unknown ids.
var ErrSessionNotFound = errors.New("session not found")

// ErrBadName is returned by Create/Rename on empty or too-long names.
var ErrBadName = errors.New("invalid session name")

// Maximum session name length (after trim) accepted by Create / Rename.
const MaxNameLength = 64

// Config is the immutable dependency block for NewManager.
type Config struct {
	// DataDir is the root that holds sessions.json and sessions/<id>/.
	// Used to derive per-session pty.stream / pty.offset paths.
	DataDir string

	Store        *store.Store
	SessionsFile *store.SessionsFile
	Runner       tmuxio.TmuxRunner

	// Nonce is the per-process sentinel nonce shared by every TmuxShell
	// the Manager creates. Generated once at boot.
	Nonce string

	MaxSessions int

	Logger *slog.Logger
}

// Manager owns N sessions.
type Manager struct {
	cfg Config

	mu     sync.Mutex
	shells map[string]*shell.TmuxShell
	metas  map[string]store.SessionMeta // mirror of sessions.json

	// Multi-subscriber listener lists. Each WS connection registers its
	// own listener via AddCloseListener / AddRenameListener and removes
	// it on disconnect — N tabs == N entries. Single-Set semantics
	// would clobber earlier tabs (regression-tested against by Plan 6
	// TestWS_SessionClosed_BroadcastsToConnectedClients).
	closeListeners  []func(sessionID string)
	renameListeners []func(sessionID, newName string)
}

// NewManager validates the config but does NOT contact tmux or load
// sessions.json. Call Reconcile() after construction.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("Config.DataDir required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("Config.Store required")
	}
	if cfg.SessionsFile == nil {
		return nil, fmt.Errorf("Config.SessionsFile required")
	}
	if cfg.Runner == nil {
		return nil, fmt.Errorf("Config.Runner required")
	}
	if cfg.Nonce == "" {
		return nil, fmt.Errorf("Config.Nonce required")
	}
	if cfg.MaxSessions <= 0 {
		return nil, fmt.Errorf("Config.MaxSessions must be > 0")
	}
	if cfg.Logger == nil {
		return nil, fmt.Errorf("Config.Logger required")
	}
	return &Manager{
		cfg:    cfg,
		shells: make(map[string]*shell.TmuxShell),
		metas:  make(map[string]store.SessionMeta),
	}, nil
}

// AddCloseListener registers a callback invoked AFTER a session has
// been fully closed (tmux killed, store deleted, sessions.json saved).
// Returns a remove function — call it on WS disconnect so a new tab's
// listener doesn't get a callback into an orphaned closure.
//
// Multi-subscriber: each WS connection adds its own listener; N tabs
// produce N callbacks per close. Plan 6 (WS) uses this.
func (m *Manager) AddCloseListener(fn func(sessionID string)) (remove func()) {
	m.mu.Lock()
	m.closeListeners = append(m.closeListeners, fn)
	idx := len(m.closeListeners) - 1
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		// Identity match via pointer; the slice may have been compacted
		// by an earlier remove so we just nil it out instead of slicing.
		// Stale nil entries are filtered when firing.
		if idx < len(m.closeListeners) {
			m.closeListeners[idx] = nil
		}
	}
}

// AddRenameListener — same pattern as AddCloseListener.
func (m *Manager) AddRenameListener(fn func(sessionID, newName string)) (remove func()) {
	m.mu.Lock()
	m.renameListeners = append(m.renameListeners, fn)
	idx := len(m.renameListeners) - 1
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if idx < len(m.renameListeners) {
			m.renameListeners[idx] = nil
		}
	}
}

// List returns a snapshot of all current session metadata in
// creation-time-ascending order.
func (m *Manager) List() []store.SessionMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.SessionMeta, 0, len(m.metas))
	for _, mt := range m.metas {
		out = append(out, mt)
	}
	// Sort by CreatedAt ascending (oldest first).
	sortByCreatedAtAsc(out)
	return out
}

// Get returns the Shell for a session ID, or ErrSessionNotFound.
func (m *Manager) Get(sessionID string) (Shell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sh, ok := m.shells[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return sh, nil
}

// streamPath / offsetPath return the per-session file paths.
func (m *Manager) streamPath(sessionID string) string {
	return filepath.Join(m.cfg.Store.SessionDir(sessionID), "pty.stream")
}

func (m *Manager) offsetPath(sessionID string) string {
	return filepath.Join(m.cfg.Store.SessionDir(sessionID), "pty.offset")
}

// sortByCreatedAtAsc — kept as a free function for testability.
func sortByCreatedAtAsc(list []store.SessionMeta) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j-1].CreatedAt.After(list[j].CreatedAt); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
