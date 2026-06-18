# Claude Refresh Parity — Plan 2/3: HTTP + WS Wiring

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the `internal/claudestate` package built in Plan 1 into the running server: introduce a process-singleton `SessionManager`, expose the new `GET /api/sessions/{id}/claude-state` endpoint, and route every inbound WS event through `SessionState.Apply` before broadcasting. Frontend still runs on the old `/claude-history` endpoint after this plan — Plan 3 switches it.

**Architecture:** A single `SessionManager` lives on `api.Deps`. The WS loop, instead of forwarding stream-json events verbatim, now: (1) parses them, (2) calls `manager.GetOrLoad(...).Apply(...)`, (3) only then writes the `claude_event` frame to the client. The new `claude_state` HTTP endpoint serves a deep-copied snapshot of the in-memory state.

**Tech Stack:** Go 1.21+, chi, stdlib `singleflight`, existing `internal/claude` parser + bridge.

**Spec:** `docs/superpowers/specs/2026-06-18-claude-refresh-parity-design.md`

**Branch:** continue on `refactor/refresh-parity`

**Prerequisite:** Plan 1 fully merged (the `internal/claudestate` package and its tests pass).

---

## File Structure

| Path | Purpose |
|---|---|
| `internal/claudestate/manager.go` | `SessionManager` singleton; per-session `GetOrLoad` via `singleflight.Group`; `Shutdown` flushes every Persister. |
| `internal/claudestate/manager_test.go` | Concurrency (100 goroutines, 1 Load), shutdown ordering, error propagation. |
| `internal/claudestate/paths.go` | Snapshot path resolution (`<dataDir>/sessions/<id>/claude.json`); jsonl path lookup via existing `claudehistory.Locator`. Trivial but isolated for testability. |
| `internal/claudestate/paths_test.go` | Path-shape assertions. |
| `internal/api/claude_state_handler.go` | New endpoint `GET /api/sessions/{sid}/claude-state`. |
| `internal/api/claude_state_handler_test.go` | Handler tests: happy path, unknown session, error. |
| `internal/api/router.go` | Register the new route; thread `*claudestate.SessionManager` through `Deps`. |
| `internal/api/ws.go` | Route inbound stream-json events through `Apply`; emit `turn_started` after `BeginTurn`; emit `tool_decision_applied` after `Apply(tool_decision)`. |
| `internal/api/ws_claude_state_test.go` | New WS frame round-trips. |
| `internal/api/claude_history_handler.go` | Add `Deprecation: true` and `Sunset:` headers; otherwise unchanged (Plan 3 removes the endpoint entirely in a follow-up release). |
| `cmd/alfred-server/main.go` | Construct the singleton `SessionManager`, attach to `Deps`, defer `Shutdown` on SIGTERM. |

**Out of scope:**
- Frontend (Plan 3)
- Removing `/claude-history` endpoint (deferred to Plan 3 / next release after rollout)
- WS protocol version bump (we add fields to existing frames; backward compatible)

---

## Task 1: `paths.go` — snapshot path resolver

**Files:**
- Create: `internal/claudestate/paths.go`
- Create: `internal/claudestate/paths_test.go`

- [ ] **Step 1: Write the failing test**

```go
package claudestate

import (
	"path/filepath"
	"testing"
)

func TestSnapshotPath_StructureIsStable(t *testing.T) {
	got := SnapshotPath("/data", "01KVBX535FVFNH6SHF8P5WZ54B")
	want := filepath.Join("/data", "sessions", "01KVBX535FVFNH6SHF8P5WZ54B", "claude.json")
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestSnapshotPath_RejectsEmptyID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty session id")
		}
	}()
	SnapshotPath("/data", "")
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestSnapshotPath -v
```

Expected: build failure — `SnapshotPath` undefined.

- [ ] **Step 3: Write `paths.go`**

```go
package claudestate

import (
	"path/filepath"
)

// SnapshotPath returns the on-disk location of the claude.json snapshot
// for a given Alfred session. dataDir is the same root the session
// manager already uses (~/.alfred or similar). Centralised here so
// every consumer agrees on the layout — and so tests don't recompute
// the path by hand.
//
// Panics on an empty session id. That's a programmer error: a caller
// shouldn't be asking for the snapshot path of "nothing." Erroring at
// the boundary keeps later code simpler (no nil checks downstream).
func SnapshotPath(dataDir, sessionID string) string {
	if sessionID == "" {
		panic("claudestate.SnapshotPath: empty sessionID")
	}
	return filepath.Join(dataDir, "sessions", sessionID, "claude.json")
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -run TestSnapshotPath -v
```

Expected: 2 PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/paths.go internal/claudestate/paths_test.go
git commit -m "feat(claudestate): SnapshotPath helper for per-session snapshot location

Centralises <dataDir>/sessions/<sid>/claude.json so manager, loader,
and handler agree. Panics on empty sessionID — programmer error at
the boundary, not a runtime nil to chase later.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 2: `SessionManager` with `GetOrLoad`

**Files:**
- Create: `internal/claudestate/manager.go`
- Create: `internal/claudestate/manager_test.go`

- [ ] **Step 1: Write the failing test**

```go
package claudestate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// GetOrLoad returns a stable *SessionState across calls — caching
// behaviour is essential for downstream consumers like the WS loop
// that look up the same session many times per second.
func TestSessionManager_GetOrLoad_ReturnsSameInstance(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &fakeJsonlLocator{})
	defer m.Shutdown(context.Background())

	a, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("expected the same *SessionState across GetOrLoad calls")
	}
}

// SingleFlight collapses concurrent first-access into one Load.
func TestSessionManager_GetOrLoad_SingleflightUnderContention(t *testing.T) {
	dir := t.TempDir()
	loc := &fakeJsonlLocator{}
	loc.delay = 25 * time.Millisecond
	m := NewSessionManager(dir, loc)
	defer m.Shutdown(context.Background())

	var wg sync.WaitGroup
	const N = 100
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = m.GetOrLoad("sess-x", "uuid-x")
		}()
	}
	wg.Wait()
	if got := loc.calls.Load(); got != 1 {
		t.Errorf("locator called %d times; want 1 (singleflight collapsed)", got)
	}
}

// Shutdown flushes the per-session Persister and prevents further
// GetOrLoad calls.
func TestSessionManager_Shutdown_FlushesAndCloses(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &fakeJsonlLocator{})
	st, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	st.BeginTurn("u1", "hi", time.Now().UTC())

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Snapshot file must exist on disk.
	snap := SnapshotPath(dir, "sess1")
	if _, err := os.Stat(snap); err != nil {
		t.Errorf("snapshot missing post-shutdown: %v", err)
	}
	// Post-shutdown GetOrLoad fails.
	if _, err := m.GetOrLoad("sess2", "uuid-2"); err == nil {
		t.Error("post-shutdown GetOrLoad should return error")
	}
}

// ---- fakes ----

type fakeJsonlLocator struct {
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeJsonlLocator) Locate(claudeUUID string) (string, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	// Return a path that doesn't exist; the loader degrades gracefully
	// to empty state when jsonl is missing.
	return filepath.Join(os.TempDir(), "nonexistent.jsonl"), nil
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestSessionManager -v
```

Expected: build failure — `NewSessionManager`, `Shutdown`, `JsonlLocator` interface undefined.

- [ ] **Step 3: Write `manager.go`**

```go
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
// detail into its tests. The production wiring (Plan 2 Task 7)
// adapts claudehistory.Locator into this interface.
type JsonlLocator interface {
	// Locate returns the jsonl file path for a given Claude session
	// uuid, or an error if not found.
	Locate(claudeUUID string) (string, error)
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
		if p, err := m.locator.Locate(claudeUUID); err == nil {
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
```

Add `golang.org/x/sync` to go.mod if not already present:

```
go get golang.org/x/sync
go mod tidy
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -run TestSessionManager -v
```

Expected: 3 PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/manager.go internal/claudestate/manager_test.go go.mod go.sum
git commit -m "feat(claudestate): SessionManager with singleflight GetOrLoad

Process-singleton registry of *SessionState. First access constructs
the session under singleflight (100 concurrent GetOrLoads collapse to
one Load). Shutdown flushes every attached Persister synchronously
and refuses further GetOrLoad calls. snapshot.json lives at
<dataDir>/sessions/<sid>/claude.json.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 3: HTTP endpoint `GET /api/sessions/{sid}/claude-state`

**Files:**
- Create: `internal/api/claude_state_handler.go`
- Create: `internal/api/claude_state_handler_test.go`

- [ ] **Step 1: Write the failing test**

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/claudestate"
)

// stub locator that always says "not found"
type stubLocator struct{}

func (stubLocator) Locate(string) (string, error) { return "", os.ErrNotExist }

func TestClaudeStateHandler_HappyPath(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	// Seed one turn so the response is non-trivial.
	st, err := mgr.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	st.BeginTurn("u1", "hi", time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC))

	r := httptest.NewRequest(http.MethodGet,
		"/api/sessions/sess1/claude-state", nil)
	// chi URLParam normally comes from routing; for direct handler
	// invocation we hydrate it via chi's RouteContext.
	r = withChiParam(r, "sid", "sess1")
	w := httptest.NewRecorder()

	GetClaudeStateHandler(mgr, &stubMetaResolver{uuid: "uuid-1"}).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var got claudestate.ClaudeState
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if len(got.Turns) != 1 || got.Turns[0].ID != "u1" {
		t.Errorf("turns: %+v", got.Turns)
	}
	if !got.TurnsLoaded {
		t.Error("TurnsLoaded should be true")
	}
}

func TestClaudeStateHandler_UnknownSessionReturns404(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	r := httptest.NewRequest(http.MethodGet, "/api/sessions/ghost/claude-state", nil)
	r = withChiParam(r, "sid", "ghost")
	w := httptest.NewRecorder()

	GetClaudeStateHandler(mgr, &stubMetaResolver{notFound: true}).ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d, want 404", w.Code)
	}
}

// withChiParam injects {sid} into the chi route context so handler
// code that calls chi.URLParam(r, "sid") works under httptest.
func withChiParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

type stubMetaResolver struct {
	uuid     string
	notFound bool
}

func (s *stubMetaResolver) ClaudeUUIDFor(sessionID string) (string, error) {
	if s.notFound {
		return "", ErrUnknownSession
	}
	return s.uuid, nil
}

// Stable imports for the test file.
var _ = filepath.Join
```

Add the missing `os` and `chi` imports — the test file's full imports block is:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jesseliu/headless-alfred/internal/claudestate"
)
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/api/... -run TestClaudeStateHandler -v
```

Expected: build failure — `GetClaudeStateHandler`, `MetaResolver`, `ErrUnknownSession` undefined.

- [ ] **Step 3: Write `claude_state_handler.go`**

```go
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
// cycle with Deprecation headers (see Task 5).
func GetClaudeStateHandler(mgr *claudestate.SessionManager, meta MetaResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if sid == "" {
			http.Error(w, `{"code":"bad_request","message":"missing sid"}`, http.StatusBadRequest)
			return
		}
		uuid, err := meta.ClaudeUUIDFor(sid)
		if errors.Is(err, ErrUnknownSession) {
			http.Error(w, `{"code":"unknown_session","message":"no such session"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("claude-state: meta resolve", "sid", sid, "err", err)
			http.Error(w, `{"code":"meta_error","message":"resolve failed"}`, http.StatusInternalServerError)
			return
		}
		st, err := mgr.GetOrLoad(sid, uuid)
		if err != nil {
			slog.Error("claude-state: load", "sid", sid, "err", err)
			http.Error(w, `{"code":"load_failed","message":"load failed"}`, http.StatusInternalServerError)
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
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/api/... -run TestClaudeStateHandler -v
```

Expected: 2 PASS.

- [ ] **Step 5: Commit**

```
git add internal/api/claude_state_handler.go internal/api/claude_state_handler_test.go
git commit -m "feat(api): GET /api/sessions/{sid}/claude-state

Serves the full ClaudeState (turns + derived inFlight + transient slots
init to empty) by deep-copying the in-memory state under read lock and
JSON-encoding it. MetaResolver abstracts the Alfred session id → Claude
uuid lookup so the handler doesn't pull session.Manager into its tests.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 4: Adapt `session.Manager` to `MetaResolver` + `JsonlLocator`

**Files:**
- Modify: `internal/api/router.go` (add adapters)
- Modify: `internal/api/router.go` (thread `*claudestate.SessionManager` through `Deps`)
- Create: `internal/api/router_adapters_test.go`

- [ ] **Step 1: Write the failing test**

```go
package api

import (
	"errors"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/session"
)

func TestSessionMetaResolver_FindsClaudeUUID(t *testing.T) {
	// Build a minimal session.Manager around a tmpdir.
	dir := t.TempDir()
	m, err := session.NewManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	// Stub a claude convo id onto the session.
	if err := m.SetClaudeConvoID(meta.ID, "claude-uuid-123"); err != nil {
		t.Fatal(err)
	}

	resolver := NewSessionMetaResolver(m)
	got, err := resolver.ClaudeUUIDFor(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "claude-uuid-123" {
		t.Errorf("got %q", got)
	}
}

func TestSessionMetaResolver_UnknownSessionReturnsErr(t *testing.T) {
	dir := t.TempDir()
	m, err := session.NewManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	resolver := NewSessionMetaResolver(m)
	_, err = resolver.ClaudeUUIDFor("nonexistent")
	if !errors.Is(err, ErrUnknownSession) {
		t.Errorf("err: %v, want ErrUnknownSession", err)
	}
}
```

If `session.Manager.SetClaudeConvoID` doesn't exist as written in your codebase, substitute the correct mutator name in the test. The contract is: "store the claude uuid for a session"; the existing code already has a setter — find it via `grep -n "ClaudeConvoID\|ConvoID" internal/session/*.go` and adjust the test call accordingly.

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/api/... -run TestSessionMetaResolver -v
```

Expected: build failure — `NewSessionMetaResolver` undefined.

- [ ] **Step 3: Append adapters to `router.go`**

```go
// SessionMetaResolver adapts session.Manager into the MetaResolver
// interface the claude-state handler depends on. Returns
// ErrUnknownSession when the session isn't present so the handler
// can map it to 404.
type SessionMetaResolver struct {
	mgr *session.Manager
}

func NewSessionMetaResolver(m *session.Manager) *SessionMetaResolver {
	return &SessionMetaResolver{mgr: m}
}

func (r *SessionMetaResolver) ClaudeUUIDFor(sessionID string) (string, error) {
	meta, err := r.mgr.Get(sessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		return "", ErrUnknownSession
	}
	if err != nil {
		return "", err
	}
	// meta carries the current Claude conversation id (rotates on /compact).
	return meta.ClaudeConvoID, nil
}

// JsonlLocatorAdapter wraps claudehistory.Locator to satisfy the
// claudestate.JsonlLocator interface, hiding the slightly different
// method signature.
type JsonlLocatorAdapter struct {
	inner *claudehistory.Locator
}

func NewJsonlLocatorAdapter(inner *claudehistory.Locator) *JsonlLocatorAdapter {
	return &JsonlLocatorAdapter{inner: inner}
}

func (a *JsonlLocatorAdapter) Locate(claudeUUID string) (string, error) {
	return a.inner.Locate(claudeUUID)
}
```

The exact field name on `meta` (e.g. `ClaudeConvoID`) may differ — open `internal/session/manager.go` and use whichever field already stores the uuid. If a getter is more idiomatic, use that.

Add `errors` and the appropriate package imports to `router.go` if not already present.

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/api/... -run TestSessionMetaResolver -v
```

Expected: 2 PASS. If `meta.ClaudeConvoID` doesn't compile, adjust to the real field name and rerun.

- [ ] **Step 5: Commit**

```
git add internal/api/router.go internal/api/router_adapters_test.go
git commit -m "feat(api): adapters from session.Manager + claudehistory.Locator

Two small adapter types so the new claude-state handler and the
SessionManager don't pull richer packages into their test helpers:
SessionMetaResolver implements MetaResolver, JsonlLocatorAdapter
implements claudestate.JsonlLocator.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 5: Wire `Deps`, register the route, deprecate the old one

**Files:**
- Modify: `internal/api/router.go` (`Deps` + `NewRouter`)
- Modify: `internal/api/claude_history_handler.go` (add deprecation headers)

- [ ] **Step 1: Write the failing test**

```go
// In a new file or appended to claude_history_handler_test.go:

func TestClaudeHistoryHandler_EmitsDeprecationHeaders(t *testing.T) {
	dir := t.TempDir()
	m, err := session.NewManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	meta, _ := m.Create("x")

	r := httptest.NewRequest(http.MethodGet,
		"/api/sessions/"+meta.ID+"/claude-history", nil)
	r = withChiParam(r, "sid", meta.ID)
	w := httptest.NewRecorder()

	GetClaudeHistoryHandler(m, claudehistory.NewLocator()).ServeHTTP(w, r)

	if got := w.Header().Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation header: %q, want true", got)
	}
	if got := w.Header().Get("Sunset"); got == "" {
		t.Error("Sunset header missing")
	}
	if got := w.Header().Get("Link"); got == "" {
		t.Error("Link header missing (should point to /claude-state)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/api/... -run TestClaudeHistoryHandler_EmitsDeprecation -v
```

Expected: FAIL — current handler emits no Deprecation header.

- [ ] **Step 3: Add deprecation headers to `claude_history_handler.go`**

Locate the top of `GetClaudeHistoryHandler`'s returned `http.HandlerFunc` and add at the start:

```go
// RFC 9745 Deprecation header. Plan 3 (frontend cutover) flips
// the UI to /claude-state; this endpoint stays one release
// cycle so older browser sessions don't break, then is removed.
w.Header().Set("Deprecation", "true")
w.Header().Set("Sunset", "Wed, 31 Dec 2026 00:00:00 GMT")
w.Header().Set("Link",
	`</api/sessions/{sid}/claude-state>; rel="successor-version"`)
```

Pick a Sunset date that's reasonable for your release cadence — at least one release window past the rollout date.

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/api/... -run TestClaudeHistoryHandler_EmitsDeprecation -v
```

Expected: PASS.

- [ ] **Step 5: Thread the manager through `Deps` + register the route**

Edit `internal/api/router.go`:

```go
type Deps struct {
	Manager        *session.Manager
	Auth           auth.Auth
	RateLimiter    *auth.RateLimiter
	Ready          func() bool
	Bridge         *claude.Bridge
	Dispatcher     *claude.Dispatcher
	RecapUpdates   <-chan string
	PVCLimitBytes  uint64

	// ClaudeStateManager is the process-singleton state registry
	// constructed in cmd/alfred-server/main.go (Task 7). The router
	// shares it across the HTTP claude-state handler and the WS
	// inbound event router.
	ClaudeStateManager *claudestate.SessionManager
}
```

And register the route inside the authed group, next to the existing claude-history line:

```go
		// Claude UI chat state (server-authoritative; persisted snapshot
		// + jsonl merge). Replaces /claude-history.
		r.Get("/api/sessions/{sid}/claude-state",
			GetClaudeStateHandler(
				d.ClaudeStateManager,
				NewSessionMetaResolver(d.Manager),
			).ServeHTTP)
```

Add `"github.com/jesseliu/headless-alfred/internal/claudestate"` to router.go's imports.

- [ ] **Step 6: Compile-check**

```
go build ./...
```

Expected: clean build.

- [ ] **Step 7: Commit**

```
git add internal/api/router.go internal/api/claude_history_handler.go internal/api/claude_history_handler_test.go
git commit -m "feat(api): register /claude-state route; deprecate /claude-history

Deps gains ClaudeStateManager so the HTTP route and (Task 6) the WS
loop share one registry. The old /claude-history endpoint stays
functional and unchanged in body but now emits Deprecation/Sunset/Link
headers per RFC 9745 so old clients can warn and new clients know
where to go.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 6: Route inbound WS events through `Apply`

**Files:**
- Modify: `internal/api/ws.go` (claude_event handling path + new outgoing frames)
- Create: `internal/api/ws_claude_state_test.go`

- [ ] **Step 1: Write the failing test**

```go
package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/claudestate"
)

// One inbound claude_event applied via the manager updates the in-memory
// state. The same event also gets broadcast back to the client.
func TestWS_ClaudeEvent_RoutedThroughApply(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	st, err := mgr.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	st.BeginTurn("u1", "hi", time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC))

	written := captureWriter()
	env := claudeEventEnvelope{
		sessionID: "sess1",
		kind:      "text_delta",
		payload: json.RawMessage(`{"index":0,"text":"hello"}`),
	}

	// New entry point we'll add to ws.go: routes the inbound event
	// through Apply then broadcasts the same payload.
	dispatchClaudeStreamEvent(env, mgr, written.write)

	if len(written.frames) != 1 {
		t.Fatalf("frames: %d", len(written.frames))
	}
	if written.frames[0].Type != "claude_event" ||
		written.frames[0].EventKind != "text_delta" {
		t.Errorf("frame: %+v", written.frames[0])
	}
	st.View(func(s *claudestate.ClaudeState) {
		if s.Turns[0].Blocks[0].Text != "hello" {
			t.Errorf("state not updated: %+v", s.Turns[0].Blocks)
		}
	})
}

// Inbound tool_decision message updates state AND emits
// tool_decision_applied so other connected tabs see the change.
func TestWS_ToolDecision_BroadcastsApplied(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	st, _ := mgr.GetOrLoad("sess1", "uuid-1")
	st.BeginTurn("u1", "hi", time.Now().UTC())
	// Seed an in-progress tool block.
	must2(t, st.Apply(claudestate.Event{
		Kind:      claudestate.EventToolUseStart,
		Timestamp: time.Now().UTC(),
		Payload:   &claudestate.ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"},
	}))

	written := captureWriter()
	dispatchToolDecision("sess1", "tu_1", "deny", "user said no", mgr, written.write)

	if len(written.frames) != 1 {
		t.Fatalf("frames: %d", len(written.frames))
	}
	if written.frames[0].Type != "tool_decision_applied" {
		t.Errorf("frame: %+v", written.frames[0])
	}
	st.View(func(s *claudestate.ClaudeState) {
		got := s.Turns[0].Blocks[0].Tool.Decision
		if got != "deny" {
			t.Errorf("decision: %q", got)
		}
	})
}

// captureWriter is a test helper that buffers OutMsgs sent via write.
type writerCapture struct {
	frames []OutMsg
}

func captureWriter() *writerCapture {
	return &writerCapture{}
}

func (w *writerCapture) write(msg OutMsg) error {
	w.frames = append(w.frames, msg)
	return nil
}

func must2(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/api/... -run TestWS_ -v
```

Expected: build failure — `dispatchClaudeStreamEvent` / `dispatchToolDecision` undefined.

- [ ] **Step 3: Add dispatch helpers to `ws.go`**

```go
// dispatchClaudeStreamEvent routes one Claude stream-json event
// through the SessionManager's Apply so server state is the truth
// source, then emits a claude_event frame to the client. The
// timestamp on the frame is the server's Apply-time wall clock —
// the same value the snapshot will persist and the same value the
// frontend reducer will project into state fields like StartedAt.
func dispatchClaudeStreamEvent(env claudeEventEnvelope, mgr *claudestate.SessionManager, write func(OutMsg) error) {
	st, err := mgr.GetOrLoad(env.sessionID, env.claudeUUID)
	if err != nil {
		slog.Warn("ws: get state for stream event", "sid", env.sessionID, "err", err)
		_ = write(OutMsg{Type: "claude_event", SessionID: env.sessionID, EventKind: string(env.kind), Payload: env.payload})
		return
	}
	ev, err := buildEventFromEnvelope(env)
	if err != nil {
		slog.Warn("ws: build event", "sid", env.sessionID, "err", err)
		_ = write(OutMsg{Type: "claude_event", SessionID: env.sessionID, EventKind: string(env.kind), Payload: env.payload})
		return
	}
	if err := st.Apply(ev); err != nil {
		slog.Warn("ws: apply event", "sid", env.sessionID, "kind", env.kind, "err", err)
	}
	// Re-encode payload so the wire format includes the server timestamp.
	payload, _ := json.Marshal(ev.Payload)
	_ = write(OutMsg{
		Type:      "claude_event",
		SessionID: env.sessionID,
		EventKind: string(env.kind),
		Payload:   payload,
		Timestamp: ev.Timestamp,
	})
}

// dispatchToolDecision applies the user's tool decision to in-memory
// state and emits a tool_decision_applied frame. The bridge resolution
// (telling the PreToolUse hook to allow or deny) still happens through
// the existing bridge path — this helper only owns the state side.
func dispatchToolDecision(sessionID, toolUseID, decision, reason string, mgr *claudestate.SessionManager, write func(OutMsg) error) {
	st, err := mgr.GetOrLoad(sessionID, "")
	if err != nil {
		slog.Warn("ws: get state for decision", "sid", sessionID, "err", err)
		return
	}
	ev := claudestate.Event{
		Kind:      claudestate.EventToolDecision,
		Timestamp: time.Now().UTC(),
		Payload: &claudestate.ToolDecisionPayload{
			ToolUseID: toolUseID,
			Decision:  decision,
			Reason:    reason,
		},
	}
	if err := st.Apply(ev); err != nil {
		slog.Warn("ws: apply decision", "sid", sessionID, "err", err)
		return
	}
	_ = write(OutMsg{
		Type:      "tool_decision_applied",
		SessionID: sessionID,
		ToolUseID: toolUseID,
		Decision:  decision,
		Timestamp: ev.Timestamp,
	})
}

// buildEventFromEnvelope turns a raw envelope (kind string + RawMessage
// payload) into a typed claudestate.Event. The timestamp is generated
// here — it's the server's Apply-time moment.
func buildEventFromEnvelope(env claudeEventEnvelope) (claudestate.Event, error) {
	now := time.Now().UTC()
	ev := claudestate.Event{Kind: claudestate.EventKind(env.kind), Timestamp: now}
	// Use the union's UnmarshalJSON to decode into the right payload struct.
	wire := struct {
		Kind      claudestate.EventKind `json:"kind"`
		Timestamp time.Time             `json:"timestamp"`
		Payload   json.RawMessage       `json:"payload"`
	}{
		Kind:      ev.Kind,
		Timestamp: now,
		Payload:   env.payload,
	}
	b, err := json.Marshal(wire)
	if err != nil {
		return ev, err
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}
```

Add the `claudestate` and `time` imports to `ws.go` if missing.

Extend the existing envelope type to carry `claudeUUID`:

```go
type claudeEventEnvelope struct {
	sessionID  string
	claudeUUID string
	kind       claude.EventKind
	payload    json.RawMessage
}
```

Update the producer (the goroutine that reads from `runner.Events`) to populate `claudeUUID` from the session's metadata. Look for where `claudeEvents <- claudeEventEnvelope{...}` is sent and extend the literal.

Replace the existing `case fwd := <-claudeEvents:` branch (around line 315 in ws.go) with:

```go
case fwd := <-claudeEvents:
	dispatchClaudeStreamEvent(fwd, claudeStateMgr, write)
```

Where `claudeStateMgr` is the new field on the WSHandler captured during construction. Update `WSHandler`'s signature:

```go
func WSHandler(
	m *session.Manager,
	a auth.Auth,
	bridge *claude.Bridge,
	disp *claude.Dispatcher,
	broadcaster *recapBroadcaster,
	disk *diskBroadcaster,
	csMgr *claudestate.SessionManager,
) http.Handler
```

Update router.go to pass `d.ClaudeStateManager` to `WSHandler(...)`.

Update the existing `handleToolDecision` to call `dispatchToolDecision` alongside (or instead of) the existing bridge resolution:

```go
case "tool_decision":
	handleToolDecision(msg, bridge, write)
	dispatchToolDecision(msg.SessionID, msg.ToolUseID, msg.Decision, msg.Reason, claudeStateMgr, write)
```

Add a `Timestamp time.Time \`json:"timestamp,omitempty"\`` field to `OutMsg` in `wsproto.go` if it doesn't already exist — the dispatch helpers populate it on the new frames.

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/api/... -run TestWS_ -v
```

Expected: 2 PASS.

- [ ] **Step 5: Compile-check the whole module + race**

```
go build ./... && go test ./internal/api/... -race -v
```

Expected: clean.

- [ ] **Step 6: Commit**

```
git add internal/api/ws.go internal/api/ws_claude_state_test.go internal/api/wsproto.go internal/api/router.go
git commit -m "feat(api): WS routes claude events + tool_decision through Apply

Inbound stream-json events now hit SessionManager.Apply before the
client sees them, so server in-memory state ≡ what any client can
reconstruct from the broadcast stream. tool_decision additionally
emits a tool_decision_applied frame carrying the resolved decision +
server timestamp, so optimistic UI on the originating tab is
confirmed and other connected tabs converge.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 7: Emit `turn_started` after `BeginTurn`

**Files:**
- Modify: `internal/api/ws.go` (`handleClaudePrompt`)
- Modify: `internal/api/wsproto.go` (`ClientNonce` field)
- Append to: `internal/api/ws_claude_state_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestWS_ClaudePrompt_EmitsTurnStarted(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())
	_, _ = mgr.GetOrLoad("sess1", "uuid-1")

	written := captureWriter()
	dispatchClaudePromptBegin("sess1", "client-nonce-abc", "hi there", mgr, written.write)

	if len(written.frames) != 1 {
		t.Fatalf("frames: %d", len(written.frames))
	}
	f := written.frames[0]
	if f.Type != "turn_started" || f.ClientNonce != "client-nonce-abc" {
		t.Errorf("frame: %+v", f)
	}
	if f.TurnID == "" {
		t.Error("TurnID empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/api/... -run TestWS_ClaudePrompt_EmitsTurnStarted -v
```

Expected: build failure — `dispatchClaudePromptBegin` undefined and `ClientNonce`/`TurnID` not on `OutMsg`.

- [ ] **Step 3: Add fields + helper**

In `wsproto.go`, add to `InMsg`:

```go
// ClientNonce is set by the frontend on claude_prompt frames to
// pair the optimistic placeholder turn with the server-side turn
// the broadcast turn_started frame announces. Opaque to the server.
ClientNonce string `json:"clientNonce,omitempty"`
```

And to `OutMsg`:

```go
ClientNonce string `json:"clientNonce,omitempty"`
TurnID      string `json:"turnId,omitempty"`
Decision    string `json:"decision,omitempty"`
```

(Some of these may already exist — keep what's there, add what's missing.)

In `ws.go`, add the helper:

```go
// dispatchClaudePromptBegin creates a server-side turn id, registers
// the turn via BeginTurn, and emits the turn_started frame so the
// frontend's optimistic placeholder can be reconciled. Caller is
// responsible for actually running `claude -p` afterwards.
func dispatchClaudePromptBegin(sessionID, clientNonce, prompt string, mgr *claudestate.SessionManager, write func(OutMsg) error) string {
	st, err := mgr.GetOrLoad(sessionID, "")
	if err != nil {
		slog.Warn("ws: get state for prompt", "sid", sessionID, "err", err)
		return ""
	}
	turnID := ulid.Make().String()
	now := time.Now().UTC()
	st.BeginTurn(turnID, prompt, now)
	_ = write(OutMsg{
		Type:        "turn_started",
		SessionID:   sessionID,
		ClientNonce: clientNonce,
		TurnID:      turnID,
		Timestamp:   now,
	})
	return turnID
}
```

Inside `handleClaudePrompt` (the existing one), before kicking off the runner, replace any existing "register optimistic turn" logic with a call to `dispatchClaudePromptBegin`:

```go
turnID := dispatchClaudePromptBegin(msg.SessionID, msg.ClientNonce, msg.Prompt, claudeStateMgr, write)
_ = turnID  // currently only used for the broadcast; future tasks may persist it.
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/api/... -run TestWS_ClaudePrompt_EmitsTurnStarted -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/api/ws.go internal/api/wsproto.go internal/api/ws_claude_state_test.go
git commit -m "feat(api): emit turn_started after BeginTurn

Server generates the turn id and announces it back to the originating
client via a new turn_started frame carrying the client-supplied
clientNonce. The frontend's optimistic placeholder turn can match on
nonce and swap in the authoritative id without a round-trip to fetch
state.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 8: Wire `cmd/alfred-server/main.go`

**Files:**
- Modify: `cmd/alfred-server/main.go`

- [ ] **Step 1: Locate the construction site**

```
grep -n "Deps{" cmd/alfred-server/main.go
```

The result is the spot where `api.Deps{...}` is filled in. Open that file.

- [ ] **Step 2: Construct the SessionManager and attach to Deps**

Above the `Deps{...}` literal, add:

```go
csMgr := claudestate.NewSessionManager(
	dataDir,
	api.NewJsonlLocatorAdapter(claudehistory.NewLocator()),
)
defer func() {
	if err := csMgr.Shutdown(context.Background()); err != nil {
		slog.Error("claudestate shutdown", "err", err)
	}
}()
```

Add the field to the `Deps{...}` literal:

```go
ClaudeStateManager: csMgr,
```

Add imports:

```go
"github.com/jesseliu/headless-alfred/internal/claudestate"
```

- [ ] **Step 3: Build the server**

```
go build ./cmd/alfred-server
```

Expected: clean build.

- [ ] **Step 4: Smoke-test against a running session manually (optional)**

Start the server, hit the new endpoint with a known session id and an auth token from your dev environment:

```
TOKEN=$(cat ~/.alfred/dev-token 2>/dev/null || echo SET_MANUALLY)
SID=$(curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8090/api/sessions | jq -r '.[0].id')
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8090/api/sessions/$SID/claude-state | jq '. | {turns: (.turns|length), inFlight}'
```

Expected: a JSON object with a non-negative turn count. If the session has never been in claude mode, expect `{"turns": 0, "inFlight": false}`.

- [ ] **Step 5: Commit**

```
git add cmd/alfred-server/main.go
git commit -m "feat(alfred-server): construct + wire the claudestate SessionManager

Singleton built at startup, shared across HTTP and WS handlers via
api.Deps. Defer Shutdown so SIGTERM flushes every active session's
snapshot before the process exits.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 9: End-to-end smoke + race + commit branch progress

- [ ] **Step 1: Run the whole module's tests**

```
go test ./... 2>&1 | tail -20
```

Expected: every package PASS. `cmd/alfred-server` may still fail with the "address already in use" if you have a dev server running; that's environmental, not a regression.

- [ ] **Step 2: Run race detector across all touched packages**

```
go test ./internal/claudestate/... ./internal/api/... -race -v 2>&1 | tail -10
```

Expected: PASS, no race warnings.

- [ ] **Step 3: Push the branch**

```
git push origin refactor/refresh-parity
```

---

## Spec coverage check (self-review)

| Spec requirement | Plan 2 task |
| --- | --- |
| `SessionManager` singleton with singleflight `GetOrLoad` | Task 2 |
| Snapshot path layout `<dataDir>/sessions/<sid>/claude.json` | Task 1 |
| `GET /api/sessions/{sid}/claude-state` | Tasks 3, 5 |
| `claude-history` deprecation headers | Task 5 |
| Server stamps `Apply`-time timestamps on stream events | Task 6 |
| `tool_decision_applied` broadcast frame | Task 6 |
| `turn_started` broadcast frame + `clientNonce` | Task 7 |
| `Deps` carries the manager; main.go wires it | Tasks 5, 8 |
| Server SIGTERM flushes snapshots | Task 8 |
| Race detector covers concurrent Apply + HTTP read | Task 9 |

Out-of-scope (deferred to Plan 3):
- Frontend `useClaudeStateLoader`
- Frontend reducer simplification (remove `new Date()` calls)
- Playwright refresh-parity tests
- Type-name `mirror` doc comment between `types.go` and `types.ts`
- Removing the legacy `/claude-history` endpoint
