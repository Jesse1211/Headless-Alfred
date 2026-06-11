# Multi-session Plan 4 — session.Manager (CRUD, 8-limit, reconciliation, exit auto-close)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/session.Manager` — the only thing in the codebase that owns N `TmuxShell`s. It exposes Create/Get/List/Rename/Close, enforces the 8-session limit, persists session metadata via `store.SessionsFile`, runs startup reconciliation between `sessions.json` × `tmux ls`, and wires `OnUserExit` → automatic Close for voluntary bash exits.

**Architecture:** `Manager` holds a `map[sessionID]*TmuxShell`, a `*store.Store` for command/output persistence, a `*store.SessionsFile` for the metadata list, a `tmuxio.TmuxRunner` for tmux invocations the per-session shells don't already cover (e.g., `ListSessions` for reconciliation), and broadcast hooks for `SessionClosed` / `SessionRenamed` events that Plan 6 (WS) will subscribe to. The Manager itself has no goroutines beyond Plan 3's per-shell readLoop+poller.

**Tech Stack:** stdlib + the three pieces from Plans 1-3.

**Spec sections covered:** §4.6 (`exit` auto-close), §4.7 (reconciliation), §6.1 (REST endpoints' contract — the methods Plan 5 will wrap), §3 ("Component boundaries").

---

## File Structure

```
internal/session/
├── manager.go          # NEW: Manager type + CRUD + Reconcile + limit
├── manager_test.go     # NEW
├── shell_iface.go      # NEW: the Shell interface that abstracts TmuxShell so api/ can depend on a small surface
└── doc.go              # NEW: 8-line package doc explaining ownership boundaries
```

Why a separate `shell_iface.go`: `internal/api` (Plan 5/6) needs to
call `Write`/`Stop`/`SubscribeEvents` etc. on per-session shells.
Importing the concrete `shell.TmuxShell` from `api` couples api to the
tmux backend. By defining `session.Shell` as an interface here, the
api package only ever sees `session.Shell` and `*session.Manager`,
and the Manager is the only thing that touches the concrete TmuxShell
type.

---

## Task 1: The `Shell` interface + Manager type skeleton

Minimal type with constructor. No Create/Reconcile yet — just enough
to verify wiring and the public interface shape.

**Files:**
- Create: `internal/session/shell_iface.go`
- Create: `internal/session/doc.go`
- Create: `internal/session/manager.go`
- Create: `internal/session/manager_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/session/manager_test.go`:

```go
package session

import (
	"log/slog"
	"os"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
	"github.com/jesseliu/headless-alfred/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *tmuxio.FakeRunner) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	fr := tmuxio.NewFakeRunner()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, err := NewManager(Config{
		DataDir:      dir,
		Store:        st,
		SessionsFile: store.NewSessionsFile(dir),
		Runner:       fr,
		Nonce:        "test-nonce",
		MaxSessions:  8,
		Logger:       logger,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, fr
}

func TestManager_EmptyOnFreshConstruction(t *testing.T) {
	m, _ := newTestManager(t)
	list := m.List()
	if len(list) != 0 {
		t.Fatalf("fresh Manager should list zero sessions, got %+v", list)
	}
}

func TestManager_RejectsMissingConfig(t *testing.T) {
	_, err := NewManager(Config{})
	if err == nil {
		t.Fatal("NewManager with empty config should error")
	}
}
```

- [ ] **Step 2: Run, confirm build failure**

Run: `go test ./internal/session/ -count=1`
Expected: BUILD FAILS on `Manager`, `Config`, `NewManager` undefined.

- [ ] **Step 3: Implement skeleton**

Create `internal/session/doc.go`:

```go
// Package session owns the lifecycle of all concurrent bash sessions
// running in tmux. It is the single source of truth for "which
// sessions exist": all callers (REST handlers, WS handlers, the
// disk-writer goroutine) ask Manager, never tmux directly.
//
// Manager is safe for concurrent use. All methods complete fast (no
// blocking I/O except the underlying tmuxio + store operations).
//
// Ownership:
//   - Manager owns N TmuxShells; each TmuxShell owns one tmux session.
//   - Manager owns sessions.json (via store.SessionsFile).
//   - Manager does NOT own the tmux server process itself — tmux is
//     a daemon started lazily by the first NewSession call.
package session
```

Create `internal/session/shell_iface.go`:

```go
package session

import (
	"github.com/jesseliu/headless-alfred/internal/shell"
)

// Shell is the subset of *shell.TmuxShell that the rest of the
// codebase (notably internal/api) needs. Defining it here lets the
// api package depend on session, not on shell, and makes it cheap
// to swap implementations (single-bash fallback, in-test fakes).
type Shell interface {
	Write(cmdID, userCmd string) error
	Stop()
	CurrentCommand() *shell.RunningCommand
	SubscribeEvents(buffer int) (*shell.EventSubscriber, func())
}
```

Create `internal/session/manager.go`:

```go
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

	mu       sync.Mutex
	shells   map[string]*shell.TmuxShell
	metas    map[string]store.SessionMeta // mirror of sessions.json

	onSessionClosed  func(sessionID string)
	onSessionRenamed func(sessionID, newName string)
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

// SetCloseListener registers a callback invoked AFTER a session has
// been fully closed (tmux killed, store deleted, sessions.json saved).
// Plan 6 (WS) uses this to broadcast session_closed.
func (m *Manager) SetCloseListener(fn func(sessionID string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSessionClosed = fn
}

// SetRenameListener — same pattern as SetCloseListener.
func (m *Manager) SetRenameListener(fn func(sessionID, newName string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSessionRenamed = fn
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
```

- [ ] **Step 4: Run tests, confirm green**

Run: `go test ./internal/session/ -count=1 -v`
Expected: 2 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/
git commit -m "session: Manager skeleton + Shell interface (no Create/Reconcile yet)"
```

---

## Task 2: Create with name validation, default-naming, 8-limit

Add `Create(name string) (SessionMeta, error)`. Empty name auto-fills as `"Session N"` where N is `len(existing) + 1`. Trims whitespace, enforces MaxNameLength, enforces MaxSessions. Persists to sessions.json before returning.

**Files:**
- Modify: `internal/session/manager.go`
- Modify: `internal/session/manager_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/session/manager_test.go`:

```go
func TestManager_Create_AssignsDefaultName(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.Name != "Session 1" {
		t.Fatalf("name = %q, want Session 1", meta.Name)
	}
	if meta.ID == "" {
		t.Fatal("id should be set")
	}
	// And the second one increments.
	meta2, _ := m.Create("")
	if meta2.Name != "Session 2" {
		t.Fatalf("name = %q, want Session 2", meta2.Name)
	}
}

func TestManager_Create_KeepsCustomName(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("training")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.Name != "training" {
		t.Fatalf("name = %q", meta.Name)
	}
}

func TestManager_Create_TrimsAndRejectsEmpty(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("  training  ")
	if meta.Name != "training" {
		t.Fatalf("name = %q, want trimmed", meta.Name)
	}
	// Whitespace-only after trim → falls back to "Session N" rather
	// than erroring, since the user clearly meant "auto-name".
	meta2, err := m.Create("   ")
	if err != nil {
		t.Fatalf("whitespace-only name should auto-name, got error: %v", err)
	}
	if meta2.Name != "Session 2" {
		t.Fatalf("name = %q", meta2.Name)
	}
}

func TestManager_Create_RejectsTooLong(t *testing.T) {
	m, _ := newTestManager(t)
	longName := ""
	for i := 0; i < MaxNameLength+1; i++ {
		longName += "a"
	}
	_, err := m.Create(longName)
	if err != ErrBadName {
		t.Fatalf("expected ErrBadName, got %v", err)
	}
}

func TestManager_Create_EnforcesLimit(t *testing.T) {
	m, _ := newTestManager(t)
	for i := 0; i < 8; i++ {
		_, err := m.Create("")
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	_, err := m.Create("")
	if err != ErrSessionLimit {
		t.Fatalf("expected ErrSessionLimit, got %v", err)
	}
}

func TestManager_Create_PersistsToSessionsFile(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("training")
	sf := m.cfg.SessionsFile
	persisted, _ := sf.Load()
	if len(persisted) != 1 || persisted[0].ID != meta.ID {
		t.Fatalf("sessions.json not persisted: %+v", persisted)
	}
}

func TestManager_Create_CallsTmuxNewSessionAndPipePane(t *testing.T) {
	m, fr := newTestManager(t)
	meta, _ := m.Create("")
	calls := fr.Calls()
	sawNew, sawPipe := false, false
	for _, c := range calls {
		if c.Method == "NewSession" && c.Args[0] == meta.ID {
			sawNew = true
		}
		if c.Method == "PipePane" && c.Args[0] == meta.ID && c.Args[1] != "" {
			sawPipe = true
		}
	}
	if !sawNew || !sawPipe {
		t.Fatalf("Create did not start tmux session+pipe: %+v", calls)
	}
}
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/session/ -run TestManager_Create -count=1`
Expected: BUILD FAILS on `m.Create` undefined.

- [ ] **Step 3: Implement Create**

Append to `internal/session/manager.go`:

```go
import (
	// add to existing imports:
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)
```

(Adjust the actual import block — keep alphabetical order.)

```go
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
		// sessions.json out of sync.
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
```

(Plan 1's `Store.EnsureSessionDirs` is the public hook that creates
the session dir + commands/outputs subdirs. The store opens
`pty.stream` before any `Save` runs, so we ensure dirs explicitly.
Idempotent — the StreamReader-opened file just goes into the
already-existing dir.)

- [ ] **Step 4: Run tests, confirm green**

Run: `go test ./internal/session/ -run TestManager_Create -count=1 -v`
Expected: 7 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/manager.go internal/session/manager_test.go
git commit -m "session: Manager.Create with default-naming, trim/length check, 8-limit, persistence"
```

---

## Task 3: Rename and Close

`Rename(id, newName) error` and `Close(id) error`. Close is responsible
for: kill the tmux shell → delete the session's store directory → drop
from sessions.json → invoke the optional onSessionClosed listener.

**Files:**
- Modify: `internal/session/manager.go`
- Modify: `internal/session/manager_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/session/manager_test.go`:

```go
func TestManager_Rename_UpdatesAndPersists(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("Session 1")
	if err := m.Rename(meta.ID, "training"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	list := m.List()
	if list[0].Name != "training" {
		t.Fatalf("name not updated: %+v", list[0])
	}
	persisted, _ := m.cfg.SessionsFile.Load()
	if persisted[0].Name != "training" {
		t.Fatalf("not persisted: %+v", persisted)
	}
}

func TestManager_Rename_RejectsEmptyAndTooLong(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("Session 1")
	if err := m.Rename(meta.ID, "   "); err != ErrBadName {
		t.Fatalf("empty: expected ErrBadName, got %v", err)
	}
	long := ""
	for i := 0; i < MaxNameLength+1; i++ {
		long += "x"
	}
	if err := m.Rename(meta.ID, long); err != ErrBadName {
		t.Fatalf("too-long: expected ErrBadName, got %v", err)
	}
}

func TestManager_Rename_UnknownIDReturnsNotFound(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Rename("nope", "x"); err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestManager_Rename_FiresListener(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("Session 1")
	called := make(chan struct{}, 1)
	m.SetRenameListener(func(id, name string) {
		if id == meta.ID && name == "training" {
			called <- struct{}{}
		}
	})
	_ = m.Rename(meta.ID, "training")
	select {
	case <-called:
	default:
		t.Fatal("listener not called")
	}
}

func TestManager_Close_RemovesFromListAndDeletesStoreDir(t *testing.T) {
	m, fr := newTestManager(t)
	meta, _ := m.Create("Session 1")

	// Place a marker file in the store dir to prove RemoveAll works.
	_ = m.cfg.Store.WriteOutput(meta.ID, "marker", []byte("x"))

	if err := m.Close(meta.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("after Close list should be empty: %+v", m.List())
	}
	// Tmux session killed.
	calls := fr.Calls()
	sawKill := false
	for _, c := range calls {
		if c.Method == "KillSession" && c.Args[0] == meta.ID {
			sawKill = true
		}
	}
	if !sawKill {
		t.Fatalf("KillSession not called: %+v", calls)
	}
	// Store dir gone.
	if _, err := os.Stat(m.cfg.Store.SessionDir(meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("store dir not removed: %v", err)
	}
	// sessions.json no longer mentions it.
	persisted, _ := m.cfg.SessionsFile.Load()
	if len(persisted) != 0 {
		t.Fatalf("sessions.json not updated: %+v", persisted)
	}
}

func TestManager_Close_UnknownIDReturnsNotFound(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Close("nope"); err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestManager_Close_FiresListener(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("")
	called := make(chan string, 1)
	m.SetCloseListener(func(id string) {
		called <- id
	})
	_ = m.Close(meta.ID)
	select {
	case got := <-called:
		if got != meta.ID {
			t.Fatalf("listener got %q, want %q", got, meta.ID)
		}
	default:
		t.Fatal("listener not called")
	}
}
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/session/ -run TestManager_Rename -count=1`
Expected: BUILD FAILS on `m.Rename` undefined.

- [ ] **Step 3: Implement Rename + Close**

Append to `internal/session/manager.go`:

```go
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
	listener := m.onSessionRenamed
	m.mu.Unlock()

	if err := m.persistMetas(); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	if listener != nil {
		listener(sessionID, cleaned)
	}
	return nil
}

// Close kills the tmux session, deletes the store directory, removes
// the entry from sessions.json, and notifies the close listener.
// Idempotent in spirit: a second Close on the same id returns
// ErrSessionNotFound.
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
	listener := m.onSessionClosed
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
	if listener != nil {
		listener(sessionID)
	}
	return nil
}
```

- [ ] **Step 4: Run, confirm green**

Run: `go test ./internal/session/ -run "TestManager_(Rename|Close)" -count=1 -v`
Expected: 7 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/manager.go internal/session/manager_test.go
git commit -m "session: Manager Rename + Close with listeners + store-dir cleanup"
```

---

## Task 4: Reconcile — startup `sessions.json × tmux ls` reconciliation

The biggest task in this plan. Loads sessions.json, lists live tmux
sessions, and processes the three branches per §4.7:
1. stored ∩ live → just resume the TmuxShell (read loop picks up at `pty.offset`)
2. stored \ live → re-create tmux session (bash is new), mark any running commands as `interrupted`
3. live \ stored → kill the orphan tmux session

**Files:**
- Modify: `internal/session/manager.go`
- Modify: `internal/session/manager_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/session/manager_test.go`:

```go
func TestManager_Reconcile_StoredIntersectLive_ResumesWithoutRecreate(t *testing.T) {
	m, fr := newTestManager(t)

	// Pre-populate sessions.json + claim a live tmux session under the
	// same id (simulates "Go restart while tmux kept running").
	id := "01HXAA"
	createdAt := time.Now().UTC().Add(-time.Hour)
	_ = m.cfg.SessionsFile.Save([]store.SessionMeta{
		{ID: id, Name: "Resumed", CreatedAt: createdAt},
	})
	_ = fr.NewSession(id, "bash")
	fr.Calls() // drain Calls so we can measure post-Reconcile only

	pre := len(fr.Calls())
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	list := m.List()
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("after reconcile list = %+v", list)
	}
	// Must NOT have called NewSession again for this id.
	post := fr.Calls()
	for _, c := range post[pre:] {
		if c.Method == "NewSession" && c.Args[0] == id {
			t.Fatalf("Reconcile re-created an already-live tmux session")
		}
	}
}

func TestManager_Reconcile_StoredMinusLive_RecreatesAndMarksInterrupted(t *testing.T) {
	m, fr := newTestManager(t)

	id := "01HXAB"
	createdAt := time.Now().UTC().Add(-time.Hour)
	_ = m.cfg.SessionsFile.Save([]store.SessionMeta{
		{ID: id, Name: "Rebuilt", CreatedAt: createdAt},
	})
	// Seed a "running" command in the store; reconciliation should mark it interrupted.
	_ = m.cfg.Store.Save(id, store.Record{
		ID: "running-cmd", SessionID: id, Command: "sleep 60",
		StartedAt: time.Now().UTC(), Status: store.StatusRunning,
	})

	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	calls := fr.Calls()
	sawNew := false
	for _, c := range calls {
		if c.Method == "NewSession" && c.Args[0] == id {
			sawNew = true
		}
	}
	if !sawNew {
		t.Fatalf("Reconcile did not create the missing tmux session: %+v", calls)
	}
	// The old running command should now be Interrupted.
	rec, _ := m.cfg.Store.Get(id, "running-cmd")
	if rec.Status != store.StatusInterrupted {
		t.Fatalf("running command not marked interrupted: %s", rec.Status)
	}
}

func TestManager_Reconcile_LiveMinusStored_KillsOrphan(t *testing.T) {
	m, fr := newTestManager(t)
	// Live but unknown id.
	_ = fr.NewSession("ghost-session", "bash")
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	calls := fr.Calls()
	sawKill := false
	for _, c := range calls {
		if c.Method == "KillSession" && c.Args[0] == "ghost-session" {
			sawKill = true
		}
	}
	if !sawKill {
		t.Fatalf("orphan tmux session not killed: %+v", calls)
	}
	if len(m.List()) != 0 {
		t.Fatalf("orphan should not appear in list: %+v", m.List())
	}
}

func TestManager_Reconcile_EmptyBoth_IsNoop(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("list non-empty: %+v", m.List())
	}
}

func TestManager_Reconcile_Idempotent(t *testing.T) {
	m, fr := newTestManager(t)
	id := "01HXAA"
	createdAt := time.Now().UTC().Add(-time.Hour)
	_ = m.cfg.SessionsFile.Save([]store.SessionMeta{
		{ID: id, Name: "X", CreatedAt: createdAt},
	})
	_ = fr.NewSession(id, "bash")

	if err := m.Reconcile(); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	// Second call must not error and must not duplicate any state.
	if err := m.Reconcile(); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(m.List()) != 1 {
		t.Fatalf("after 2x Reconcile list = %+v, want 1 entry", m.List())
	}
	// The second resumeShell logs an "already started" error from
	// TmuxShell.Resume and moves on — verify our shells map still
	// holds exactly one entry for the id.
	if got, _ := m.Get(id); got == nil {
		t.Fatalf("session %s no longer accessible via Get", id)
	}
}
```

Add the `time` import to the test file if not already there.

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/session/ -run TestManager_Reconcile -count=1`
Expected: BUILD FAILS on `m.Reconcile` undefined.

- [ ] **Step 3: Implement Reconcile**

Append to `internal/session/manager.go`:

```go
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

	// stored ∩ live AND stored \ live both start by accepting the meta
	// and starting a TmuxShell (Plan 3 logic: NewTmuxShell + Start is
	// safe whether or not the tmux session already exists; if it does,
	// NewSession will fail and we treat that as "already there").
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
	if err := ts.Resume(); err != nil {
		return fmt.Errorf("resume tmux shell: %w", err)
	}
	m.mu.Lock()
	m.shells[sessionID] = ts
	m.mu.Unlock()
	return nil
}
```

`Resume` is a new method on `TmuxShell` we need to add (a thin
sibling of `Start` that skips the `NewSession`/`SetOption`/`stty
-echo`/`PipePane` setup because they were done by a previous
process). Add this method to `internal/shell/tmux_shell.go`:

```go
// Resume attaches to an already-running tmux session. The tmux
// session is assumed to have been set up by a previous process:
//   remain-on-exit on, PTY echo off, pipe-pane writing to StreamPath.
// All Resume does is wire the parser, open the StreamReader, and
// launch the read+poller goroutines.
func (ts *TmuxShell) Resume() error {
	ts.mu.Lock()
	if ts.started {
		ts.mu.Unlock()
		return fmt.Errorf("TmuxShell %s already started", ts.cfg.SessionID)
	}
	ts.started = true
	ts.mu.Unlock()

	ts.parser = NewParser(ts.cfg.Nonce)
	ts.parser.OnEvent = ts.onParserEvent

	reader, err := tmuxio.NewStreamReader(ts.cfg.StreamPath, ts.cfg.OffsetPath, parserSink{ts.parser})
	if err != nil {
		return fmt.Errorf("open stream reader: %w", err)
	}
	ts.reader = reader

	go ts.readLoop()
	go ts.poller()

	ts.cfg.Logger.Info("tmux shell resumed",
		"session", ts.cfg.SessionID,
		"resume_offset", reader.Offset(),
	)
	return nil
}
```

- [ ] **Step 4: Run, confirm green**

Run: `go test ./internal/session/ -run TestManager_Reconcile -race -count=1 -v`
Expected: 5 PASS (`StoredIntersectLive_ResumesWithoutRecreate`, `StoredMinusLive_RecreatesAndMarksInterrupted`, `LiveMinusStored_KillsOrphan`, `EmptyBoth_IsNoop`, `Idempotent`).

Also confirm Plan 3's shell tests still pass since we added Resume:

Run: `go test ./internal/shell/ -race -count=1`
Expected: all green (Plan 3 tests don't exercise Resume; we add a Resume test in this task's Step 5).

- [ ] **Step 5: Add a TmuxShell.Resume unit test**

Append to `internal/shell/tmux_shell_test.go`:

```go
func TestTmuxShell_Resume_StartsReadLoopWithoutNewSession(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	// Pretend a prior process already created the session.
	_ = fr.NewSession("sess-1", "bash")
	ts, dir := newTestTmuxShell(t, fr)
	pre := len(fr.Calls())

	if err := ts.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer ts.Close()

	calls := fr.Calls()
	for _, c := range calls[pre:] {
		if c.Method == "NewSession" {
			t.Fatalf("Resume must not call NewSession: %+v", c)
		}
		if c.Method == "PipePane" {
			t.Fatalf("Resume must not re-set pipe-pane: %+v", c)
		}
	}

	// Verify the read loop actually consumes bytes that appear in the
	// already-existing stream file.
	sub, cancel := ts.SubscribeEvents(8)
	defer cancel()
	streamFile := filepath.Join(dir, "pty.stream")
	body := "\x1eALFRED_START_nonce-x cmd-R /tmp\x1eX\nhi\n\x1eALFRED_END_nonce-x cmd-R 0\x1eX\n"
	f, _ := os.OpenFile(streamFile, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write([]byte(body))
	_ = f.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-sub.C:
			if ev.Ended != nil && ev.Ended.CmdID == "cmd-R" {
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("Resume did not deliver Ended event for cmd-R")
}
```

- [ ] **Step 6: Run, confirm green**

Run: `go test ./internal/shell/ -run TestTmuxShell_Resume -race -count=1 -v`
Expected: 1 PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/session/manager.go internal/session/manager_test.go internal/shell/tmux_shell.go internal/shell/tmux_shell_test.go
git commit -m "session: Manager.Reconcile + shell: TmuxShell.Resume for Go-restart resume"
```

---

## Plan 4 acceptance

- `go test -race ./internal/session/` is green (20+ tests across 4 tasks).
- `go test -race ./internal/shell/` is green (Plan 3's 10 + this plan's `Resume` test = 11).
- The `session.Shell` interface exposes exactly `Write`, `Stop`, `CurrentCommand`, `SubscribeEvents` — the surface api/ will depend on.
- `Manager.Reconcile()` is idempotent: running it twice in a row is safe (the second call finds stored ∩ live for every session and resumes them again — but resume itself is gated by `ts.started`, so a second Resume returns "already started" — this is harmless; the manager catches the error and logs it).
- All four error sentinels (`ErrSessionLimit`, `ErrSessionNotFound`, `ErrBadName`, plus the underlying `ErrBadName` reuse for both Create and Rename) are tested.

### Known limitations carried forward

- `Manager.Reconcile()` calling Resume twice on the same in-memory Manager returns "already started" from the second Resume; the Manager logs and moves on. Production code only calls Reconcile once at boot, so this is purely defensive. Covered by `TestManager_Reconcile_Idempotent`.
- Listener callbacks (`SetCloseListener`, `SetRenameListener`) fire synchronously, with `m.mu` released. For Close this includes disk I/O (DeleteSession) — total wall time of one Close is bounded by a few tens of milliseconds in practice. Acceptable for a single-user tool.

---

## Plan 4 self-review checklist

- [ ] `grep -rE "TODO|FIXME|XXX" internal/session/` is empty (except this file).
- [ ] `go vet ./internal/session/ ./internal/shell/` is clean.
- [ ] `go test -race -count=1 ./internal/session/ ./internal/shell/` is green.
- [ ] The `Shell` interface in shell_iface.go matches the methods api/ will actually call (cross-check against the existing internal/api/ws.go and commands.go before declaring victory).
- [ ] `git log --oneline | head -5` shows the four commits from this plan plus the Resume commit from Task 4.
