package session

import (
	"crypto/rand"
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

	// Multi-subscriber listener registries — see listenerSet in listeners.go.
	onClose  listenerSet[string]
	onRename listenerSet[renameEvent]
	onCreate listenerSet[string]
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
func (m *Manager) AddCloseListener(fn func(sessionID string)) (remove func()) {
	return m.onClose.Add(fn)
}

// AddRenameListener — same pattern as AddCloseListener.
func (m *Manager) AddRenameListener(fn func(sessionID, newName string)) (remove func()) {
	return m.onRename.Add(func(e renameEvent) { fn(e.SessionID, e.NewName) })
}

// AddCreateListener — same pattern as AddCloseListener. Fired AFTER a
// new session is fully spun up (tmux shell started, sessions.json
// persisted). WS clients use this to subscribe to the new session's
// event stream without having to reconnect.
func (m *Manager) AddCreateListener(fn func(sessionID string)) (remove func()) {
	return m.onCreate.Add(fn)
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

// DataDir returns the root data directory passed in Config.DataDir.
// Used by callers that need to compute on-disk paths (e.g. the
// per-session summary file).
func (m *Manager) DataDir() string {
	return m.cfg.DataDir
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
		Mode:      store.ModeShell,
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

	m.onCreate.Fire(id)
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
	m.startPersister(sessionID, ts)
	return nil
}

// startPersister subscribes to the shell's events and persists Ended events
// to disk. Runs for the lifetime of the shell — independent of any WS client
// — so command records get marked completed even when no one is connected
// (user disconnected before completion, or alfred restarted and a new alfred
// is consuming the pty.stream tail with no client yet attached).
//
// The shell's broadcaster closes the subscriber's channel when the shell is
// closed; the goroutine exits naturally at that point.
func (m *Manager) startPersister(sessionID string, sh Shell) {
	sub, _ := sh.SubscribeEvents(64)
	go func() {
		for ev := range sub.C {
			if ev.Ended == nil {
				continue
			}
			e := ev.Ended
			if err := m.cfg.Store.WriteOutput(sessionID, e.CmdID, e.Output); err != nil {
				m.cfg.Logger.Warn("persist output", "session", sessionID, "cmd", e.CmdID, "err", err)
			}
			rec, err := m.cfg.Store.Get(sessionID, e.CmdID)
			if err != nil {
				m.cfg.Logger.Warn("persist get", "session", sessionID, "cmd", e.CmdID, "err", err)
				continue
			}
			ec := e.ExitCode
			fa := e.FinishedAt
			rec.ExitCode = &ec
			rec.FinishedAt = &fa
			rec.OutputTruncated = e.Truncated
			if rec.Status == store.StatusRunning {
				rec.Status = store.StatusCompleted
			}
			if err := m.cfg.Store.Save(sessionID, rec); err != nil {
				m.cfg.Logger.Warn("persist save", "session", sessionID, "cmd", e.CmdID, "err", err)
			}
		}
	}()
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

// Rename changes the display name of a session. Returns ErrBadName
// for empty/over-length names, ErrSessionNotFound for unknown ids.
func (m *Manager) Rename(sessionID, newName string) error {
	cleaned := strings.TrimSpace(newName)
	if cleaned == "" || len(cleaned) > MaxNameLength {
		return ErrBadName
	}
	m.mu.Lock()
	meta, ok := m.metas[sessionID]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	meta.Name = cleaned
	m.metas[sessionID] = meta
	// Snapshot the metas slice UNDER the lock so a concurrent Close that
	// deletes the just-renamed entry can't race us into persisting a stale
	// list (previously, persistMetas re-acquired the lock and could see
	// post-Close state, silently dropping the rename from sessions.json).
	list := make([]store.SessionMeta, 0, len(m.metas))
	for _, mt := range m.metas {
		list = append(list, mt)
	}
	sortByCreatedAtAsc(list)
	m.mu.Unlock()

	if err := m.cfg.SessionsFile.Save(list); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	m.onRename.Fire(renameEvent{SessionID: sessionID, NewName: cleaned})
	return nil
}

// GetMode returns the current mode of the session. If the session is not
// found, it defaults to ModeShell (callers should have verified existence
// via List/Get first). Consistent with the spec: unknown == shell.
func (m *Manager) GetMode(sessionID string) store.SessionMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.metas[sessionID]; ok {
		return meta.Mode
	}
	return store.ModeShell
}

// SetMode atomically updates the in-memory mode and persists sessions.json.
// Returns ErrSessionNotFound if sessionID is unknown.
// Lock discipline mirrors Rename: mutate under m.mu, then persist outside.
func (m *Manager) SetMode(sessionID string, mode store.SessionMode) error {
	m.mu.Lock()
	meta, ok := m.metas[sessionID]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	meta.Mode = mode
	m.metas[sessionID] = meta
	// Snapshot the list under the lock (same pattern as Rename).
	list := make([]store.SessionMeta, 0, len(m.metas))
	for _, mt := range m.metas {
		list = append(list, mt)
	}
	sortByCreatedAtAsc(list)
	m.mu.Unlock()

	if err := m.cfg.SessionsFile.Save(list); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	return nil
}

// GetRenderer returns the per-session claude renderer ("tui", "ui",
// or "" if not in claude mode). Defaults to empty if the session
// is unknown.
func (m *Manager) GetRenderer(sessionID string) store.ClaudeRenderer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.metas[sessionID]; ok {
		return meta.Renderer
	}
	return ""
}

// SetRenderer atomically updates the in-memory renderer and persists.
// Empty string clears the renderer (used on exit from claude mode).
// Returns ErrSessionNotFound if sessionID is unknown.
func (m *Manager) SetRenderer(sessionID string, r store.ClaudeRenderer) error {
	return m.mutateAndPersist(sessionID, func(meta *store.SessionMeta) {
		meta.Renderer = r
	})
}

// GetTemplateID returns the per-session prompt template ID
// (e.g. "summary-todo"), or "" if no template is active. Empty
// for unknown sessions.
func (m *Manager) GetTemplateID(sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.metas[sessionID]; ok {
		return meta.TemplateID
	}
	return ""
}

// SetTemplateID atomically updates the in-memory template id and
// persists. Empty string clears it (no template; no injection).
// Returns ErrSessionNotFound if sessionID is unknown.
func (m *Manager) SetTemplateID(sessionID string, id string) error {
	return m.mutateAndPersist(sessionID, func(meta *store.SessionMeta) {
		meta.TemplateID = id
	})
}

// GetClaudeBypass reports whether claude_prompt invocations on this
// session should pass --dangerously-skip-permissions. Defaults to
// false for unknown sessions.
func (m *Manager) GetClaudeBypass(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.metas[sessionID]; ok {
		return meta.ClaudeBypassPermissions
	}
	return false
}

// SetClaudeBypass atomically updates the bypass flag and persists.
// Returns ErrSessionNotFound if sessionID is unknown.
func (m *Manager) SetClaudeBypass(sessionID string, bypass bool) error {
	return m.mutateAndPersist(sessionID, func(meta *store.SessionMeta) {
		meta.ClaudeBypassPermissions = bypass
	})
}

// EnsureClaudeConvoID returns the session's Claude conversation
// UUID, generating + persisting one if absent. Idempotent.
func (m *Manager) EnsureClaudeConvoID(sessionID string) (string, error) {
	m.mu.Lock()
	meta, ok := m.metas[sessionID]
	if !ok {
		m.mu.Unlock()
		return "", ErrSessionNotFound
	}
	if meta.ClaudeSessionID != "" {
		uuid := meta.ClaudeSessionID
		m.mu.Unlock()
		return uuid, nil
	}
	m.mu.Unlock()

	// Generate outside the lock — uuid is cheap but stays consistent
	// with the rest of the code's discipline of doing IO/work without
	// holding m.mu.
	newUUID := newConvoUUID()
	if err := m.mutateAndPersist(sessionID, func(meta *store.SessionMeta) {
		if meta.ClaudeSessionID == "" {
			meta.ClaudeSessionID = newUUID
		}
	}); err != nil {
		return "", err
	}
	return m.getClaudeConvoID(sessionID), nil
}

// getClaudeConvoID returns the persisted ClaudeSessionID for sessionID,
// acquiring m.mu internally. Empty if the session is unknown.
func (m *Manager) getClaudeConvoID(sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.metas[sessionID]; ok {
		return meta.ClaudeSessionID
	}
	return ""
}

// FindByClaudeConvoID returns the Alfred sessionID whose Claude
// conversation UUID matches convoID. Returns "" if no match. Used
// by the PreToolUse hook bridge: the hook payload carries Claude's
// session_id, and we need to route the approval request to the
// corresponding Alfred session's WS client. O(N) scan over sessions;
// N <= 8, so a map index is over-engineering.
func (m *Manager) FindByClaudeConvoID(convoID string) string {
	if convoID == "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for sid, meta := range m.metas {
		if meta.ClaudeSessionID == convoID {
			return sid
		}
	}
	return ""
}

// mutateAndPersist is a small refactoring helper: do an in-memory
// mutation on a session's metadata under the lock, snapshot, then
// persist outside the lock. Used by SetRenderer and
// EnsureClaudeConvoID; could replace the body of SetMode too, but
// we leave SetMode alone to minimize this diff.
func (m *Manager) mutateAndPersist(sessionID string, mut func(*store.SessionMeta)) error {
	m.mu.Lock()
	meta, ok := m.metas[sessionID]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	mut(&meta)
	m.metas[sessionID] = meta
	list := make([]store.SessionMeta, 0, len(m.metas))
	for _, mt := range m.metas {
		list = append(list, mt)
	}
	sortByCreatedAtAsc(list)
	m.mu.Unlock()
	if err := m.cfg.SessionsFile.Save(list); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	return nil
}

// newConvoUUID returns a random v4-like UUID string. Local helper
// because importing google/uuid for one call site is overkill.
func newConvoUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// extremely unlikely; fall back to time-based
		return ulid.Make().String()
	}
	// RFC 4122 v4
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Reconcile is called once at boot, before the HTTP listener opens.
// It walks sessions.json × `tmux ls` and:
//
//   - stored ∩ live: take ownership of the existing tmux session; the
//     TmuxShell's stream reader resumes from pty.offset.
//   - stored \ live: the tmux session is gone (Pod restart or tmux
//     crash). Re-create the tmux session with a fresh bash. Mark any
//     "running" command in the store as "interrupted".
//   - live \ stored: an orphan tmux session we never recorded. Kill it.
func (m *Manager) Reconcile() error {
	stored, err := m.cfg.SessionsFile.Load()
	if err != nil {
		return fmt.Errorf("load sessions.json: %w", err)
	}
	liveNames, err := m.cfg.Runner.ListSessions()
	if err != nil {
		return fmt.Errorf("list tmux sessions: %w", err)
	}
	live := make(map[string]bool, len(liveNames))
	for _, n := range liveNames {
		live[n] = true
	}
	storedIDs := make(map[string]bool, len(stored))
	for _, s := range stored {
		storedIDs[s.ID] = true
	}

	for _, meta := range stored {
		m.mu.Lock()
		m.metas[meta.ID] = meta
		m.mu.Unlock()

		if live[meta.ID] {
			// stored ∩ live: bash and pipe-pane are still running from
			// the previous Go process. Just re-attach a TmuxShell with
			// Resume (skips NewSession / PipePane setup).
			if err := m.resumeShell(meta.ID); err != nil {
				m.cfg.Logger.Error("resume tmux shell", "session", meta.ID, "err", err)
			}
			continue
		}
		// stored \ live: re-create.
		if err := m.startShell(meta.ID); err != nil {
			m.cfg.Logger.Error("recreate tmux shell", "session", meta.ID, "err", err)
		}
		// Mark any running commands as interrupted — the bash that was
		// running them is gone.
		if err := m.cfg.Store.SweepRunningToInterrupted([]string{meta.ID}); err != nil {
			m.cfg.Logger.Error("sweep running→interrupted", "session", meta.ID, "err", err)
		}
		// Force mode back to shell: any Claude process that was running is
		// dead (tmux died with the pod). A persisted mode=claude is now
		// stale — reset it so reconnecting clients render the chat view.
		if err := m.SetMode(meta.ID, store.ModeShell); err != nil && !errors.Is(err, ErrSessionNotFound) {
			m.cfg.Logger.Error("reset mode to shell after recreate", "session", meta.ID, "err", err)
		}
		// Clear the renderer too — it has meaning only when mode is
		// claude, and we just forced mode to shell. (ClaudeSessionID
		// is intentionally NOT cleared: the on-disk transcript
		// survives the pod restart, so re-entering claude resumes
		// the same conversation.)
		if err := m.SetRenderer(meta.ID, ""); err != nil && !errors.Is(err, ErrSessionNotFound) {
			m.cfg.Logger.Error("reset renderer after recreate", "session", meta.ID, "err", err)
		}
		// Same for the bypass-permissions opt-in — the user re-picks
		// it on the next Start Claude dialog.
		if err := m.SetClaudeBypass(meta.ID, false); err != nil && !errors.Is(err, ErrSessionNotFound) {
			m.cfg.Logger.Error("reset claude bypass after recreate", "session", meta.ID, "err", err)
		}
		// Same for the template id — it's an entry-time choice and
		// the per-session summary file (if any) on disk is what
		// remembers across restarts, not the template flag.
		if err := m.SetTemplateID(meta.ID, ""); err != nil && !errors.Is(err, ErrSessionNotFound) {
			m.cfg.Logger.Error("reset template id after recreate", "session", meta.ID, "err", err)
		}
	}

	// live \ stored: kill orphans.
	for _, name := range liveNames {
		if storedIDs[name] {
			continue
		}
		if err := m.cfg.Runner.KillSession(name); err != nil {
			m.cfg.Logger.Error("kill orphan tmux session", "session", name, "err", err)
		}
	}
	return nil
}

// resumeShell builds a TmuxShell for an already-live tmux session.
// Unlike startShell it does NOT call NewSession; the existing
// session and its pipe-pane are still running, so we just attach a
// new readLoop + poller and pick up at pty.offset.
//
// If the store contains a status=running record for this session,
// we seed it into the TmuxShell so WS reattach sees the in-flight
// command. The previous process emitted the START sentinel before
// dying; that byte is already past the new pty.offset, so without
// this seed the new parser would silently drop the upcoming chunks
// and END.
//
// Caller must NOT hold m.mu (we take it ourselves when registering).
func (m *Manager) resumeShell(sessionID string) error {
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
		return err
	}
	id := sessionID
	ts.OnUserExit = func() {
		_ = m.Close(id)
	}

	// Find a status=running record (there is at most one per session
	// because the shell only accepts one command at a time).
	var seed *shell.RunningCommand
	list, err := m.cfg.Store.List(sessionID, 0, "")
	if err != nil {
		m.cfg.Logger.Warn("list store for seed", "session", sessionID, "err", err)
	}
	for _, r := range list {
		if r.Status == store.StatusRunning {
			seed = &shell.RunningCommand{
				ID:        r.ID,
				Command:   r.Command,
				Cwd:       r.Cwd,
				StartedAt: r.StartedAt,
			}
			break
		}
	}

	// Ensure the session dir + commands/outputs subdirs exist BEFORE
	// the read loop opens pty.stream inside that dir. Plan 1's
	// EnsureSessionDirs is idempotent.
	if err := m.cfg.Store.EnsureSessionDirs(sessionID); err != nil {
		return fmt.Errorf("ensure session dirs: %w", err)
	}

	if err := ts.Resume(seed); err != nil {
		return fmt.Errorf("resume tmux shell: %w", err)
	}
	m.mu.Lock()
	m.shells[sessionID] = ts
	m.mu.Unlock()
	m.startPersister(sessionID, ts)
	return nil
}

// StoreFor returns the underlying Store. Used by HTTP handlers that
// need to read/write per-session command JSONs directly.
func (m *Manager) StoreFor() *store.Store {
	return m.cfg.Store
}

// Close kills the tmux session, deletes the store directory, removes
// the entry from sessions.json, and notifies every registered close
// listener. Idempotent in spirit: a second Close on the same id
// returns ErrSessionNotFound.
func (m *Manager) Close(sessionID string) error {
	m.mu.Lock()
	sh, hasShell := m.shells[sessionID]
	_, hasMeta := m.metas[sessionID]
	if !hasMeta {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	delete(m.shells, sessionID)
	delete(m.metas, sessionID)
	m.mu.Unlock()

	if hasShell {
		if err := sh.Close(); err != nil {
			m.cfg.Logger.Error("close tmux shell", "session", sessionID, "err", err)
			// continue anyway — the in-memory state is gone
		}
	}
	if err := m.cfg.Store.DeleteSession(sessionID); err != nil {
		m.cfg.Logger.Error("delete store session dir", "session", sessionID, "err", err)
	}
	if err := m.persistMetas(); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	m.onClose.Fire(sessionID)
	return nil
}
