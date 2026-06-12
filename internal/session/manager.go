package session

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

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

// Create starts a new session. An empty or whitespace-only name is
// replaced with "Session N" where N is the count of existing sessions
// plus 1. Returns ErrSessionLimit if MaxSessions is reached and
// ErrBadName for over-long names.
func (m *Manager) Create(name string) (store.SessionMeta, error) {
	cleaned := strings.TrimSpace(name)
	if len(cleaned) > MaxNameLength {
		return store.SessionMeta{}, ErrBadName
	}

	m.mu.Lock()
	if len(m.metas) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return store.SessionMeta{}, ErrSessionLimit
	}
	if cleaned == "" {
		// "Session N" where N = current count + 1. Note: if a previous
		// Create rolled back (startShell or persist failed), N will be
		// reused — the next successful Create may produce a duplicate
		// display name like "Session 2" twice. We accept this because
		// (a) display-name uniqueness is not a Manager invariant, and
		// (b) the underlying ULID id is always unique. Users can rename
		// via PATCH /api/sessions/{id} to disambiguate.
		cleaned = fmt.Sprintf("Session %d", len(m.metas)+1)
	}
	id := ulid.Make().String()
	meta := store.SessionMeta{
		ID:        id,
		Name:      cleaned,
		CreatedAt: time.Now().UTC(),
	}
	// Reserve the slot in m.metas BEFORE starting the tmux shell so a
	// concurrent Create can't blow the limit. We'll rollback on error.
	m.metas[id] = meta
	m.mu.Unlock()

	if err := m.startShell(id); err != nil {
		m.mu.Lock()
		delete(m.metas, id)
		m.mu.Unlock()
		return store.SessionMeta{}, err
	}

	if err := m.persistMetas(); err != nil {
		// Rollback the tmux session and the meta entry — we don't want
		// sessions.json out of sync. We delete from both maps under mu,
		// then call sh.Close() outside the lock to avoid holding mu
		// across the tens-of-milliseconds tmux KillSession + readLoop
		// drain. By the time we Close it, sh is goroutine-local (no
		// other code can reach it — it's no longer in m.shells), so
		// there is no concurrent-access window.
		m.mu.Lock()
		sh, ok := m.shells[id]
		delete(m.shells, id)
		delete(m.metas, id)
		m.mu.Unlock()
		if ok {
			_ = sh.Close()
		}
		return store.SessionMeta{}, fmt.Errorf("persist sessions.json: %w", err)
	}
	return meta, nil
}

// startShell spins up a brand-new TmuxShell for sessionID.
// Caller must NOT hold m.mu — this method takes the lock internally
// when registering the shell, and TmuxShell.Start can take tens of
// milliseconds which we don't want under lock.
func (m *Manager) startShell(sessionID string) error {
	cfg := shell.TmuxShellConfig{
		SessionID:  sessionID,
		Nonce:      m.cfg.Nonce,
		Runner:     m.cfg.Runner,
		StreamPath: m.streamPath(sessionID),
		OffsetPath: m.offsetPath(sessionID),
		Logger:     m.cfg.Logger,
	}
	ts, err := shell.NewTmuxShell(cfg)
	if err != nil {
		return fmt.Errorf("NewTmuxShell %s: %w", sessionID, err)
	}
	// Wire the OnUserExit hook: voluntary bash exit ⇒ Manager.Close.
	id := sessionID
	ts.OnUserExit = func() {
		_ = m.Close(id)
	}
	// Ensure the session dir + commands/outputs subdirs exist BEFORE
	// the read loop opens pty.stream inside that dir. Plan 1's
	// EnsureSessionDirs is idempotent.
	if err := m.cfg.Store.EnsureSessionDirs(sessionID); err != nil {
		return fmt.Errorf("ensure session dirs: %w", err)
	}
	if err := ts.Start(); err != nil {
		return fmt.Errorf("Start tmux shell: %w", err)
	}
	m.mu.Lock()
	m.shells[sessionID] = ts
	m.mu.Unlock()
	return nil
}

// persistMetas writes m.metas atomically to sessions.json.
func (m *Manager) persistMetas() error {
	m.mu.Lock()
	list := make([]store.SessionMeta, 0, len(m.metas))
	for _, mt := range m.metas {
		list = append(list, mt)
	}
	sortByCreatedAtAsc(list)
	m.mu.Unlock()
	return m.cfg.SessionsFile.Save(list)
}

// Close is implemented in Plan 4 Task 3. Stub here so startShell's
// OnUserExit hook closure compiles in this commit.
func (m *Manager) Close(sessionID string) error {
	// TODO(plan-4-task-3): real implementation
	return nil
}
