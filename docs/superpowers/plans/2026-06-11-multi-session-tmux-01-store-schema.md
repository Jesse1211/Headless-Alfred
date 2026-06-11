# Multi-session Plan 1 — Store schema (per-session layout + migration)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure `internal/store` so command JSON + output log files live under a per-session directory, and provide a one-shot migration that imports the old flat layout into a synthetic "Imported" session. No other packages change in this plan.

**Architecture:** `Store` keeps its current `Save/Get/List/WriteOutput/ReadOutput/SweepRunningToInterrupted` surface — but now every method takes a `sessionID` argument. The directory layout becomes `<data>/sessions/<sessionID>/commands/*.json` and `<data>/sessions/<sessionID>/outputs/*.log`. A separate `SessionsFile` helper owns reads/writes of `<data>/sessions.json` (the metadata list). A `MigrateLegacyLayout` function detects the old flat layout (`<data>/commands/`, `<data>/outputs/`) and folds it into a single fresh session named "Imported".

**Tech Stack:** stdlib only (`encoding/json`, `os`, `path/filepath`, `errors`, `sort`). No new go.mod entries.

**Spec sections covered:** §5 (storage layout), §5 (schema migration). This plan does not touch tmux, the shell package, or any HTTP/WS surface.

---

## File Structure

```
internal/store/
├── record.go            # MODIFY: add SessionID field
├── store.go             # MODIFY: every public method gains sessionID arg
├── store_test.go        # MODIFY: rewrite to pass sessionID; add list-by-session test
├── sessions.go          # NEW: SessionsFile (sessions.json reader/writer)
├── sessions_test.go     # NEW
├── migrate.go           # NEW: MigrateLegacyLayout
└── migrate_test.go      # NEW
```

Why split `sessions.go` and `migrate.go` out: each has a single responsibility and is independently testable. `Store` doesn't know about `SessionsFile` (different file lifecycles, no shared mutexes). `MigrateLegacyLayout` is a one-shot startup utility, not a normal operation — keeping it apart documents that.

---

## Task 1: Add SessionID to Record + update existing tests

The smallest possible change to confirm the schema migration round-trips through JSON.

**Files:**
- Modify: `internal/store/record.go`
- Modify: `internal/store/store_test.go` (the `TestStore_SaveAndGet` test will start failing — update it)

- [ ] **Step 1: Add the field**

Edit `internal/store/record.go` — after `ID string ...`:

```go
type Record struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	// ... rest unchanged ...
}
```

- [ ] **Step 2: Update TestStore_SaveAndGet to include a SessionID and assert it round-trips**

Edit `internal/store/store_test.go` — change the `TestStore_SaveAndGet` body. The Save / Get signatures change in Task 2, so for now keep them as-is but add the field to the literal:

```go
func TestStore_SaveAndGet(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := Record{
		ID:        "01HAB",
		SessionID: "sess-1",
		Command:   "ls",
		Cwd:       "/tmp",
		StartedAt: now,
		Status:    StatusRunning,
	}
	if err := s.Save(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Get("01HAB")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("session_id not round-tripped: %q", got.SessionID)
	}
	if got.Command != "ls" || got.Status != StatusRunning {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
```

- [ ] **Step 3: Run tests, confirm SaveAndGet passes and others still pass**

Run: `go test -run TestStore ./internal/store/ -count=1`
Expected: all 10 existing tests PASS. We haven't changed any signatures yet — this is just the JSON field plumbing.

- [ ] **Step 4: Commit**

```bash
git add internal/store/record.go internal/store/store_test.go
git commit -m "store: add SessionID to Record (schema only; no behavior change)"
```

---

## Task 2: Make Store methods session-scoped

Now we move the directories around and change every method signature to require a sessionID.

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Rewrite Store to be session-scoped**

Replace the contents of `internal/store/store.go` with:

```go
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

var ErrNotFound = errors.New("record not found")

// Store owns the filesystem layout:
//
//	<dir>/sessions/<sessionID>/commands/<cmdID>.json
//	<dir>/sessions/<sessionID>/outputs/<cmdID>.log
//
// Every method takes a sessionID. Pass the same value used in
// Record.SessionID; the store does no cross-validation (callers are
// expected to know their own session).
type Store struct {
	dir string
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir sessions: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

// SessionDir returns the absolute path of the session's root directory.
// The directory may not exist yet; callers ensure that via Save/WriteOutput
// (both call ensureSessionDirs internally).
func (s *Store) SessionDir(sessionID string) string {
	return filepath.Join(s.dir, "sessions", sessionID)
}

func (s *Store) commandPath(sessionID, id string) string {
	return filepath.Join(s.SessionDir(sessionID), "commands", id+".json")
}

func (s *Store) outputPath(sessionID, id string) string {
	return filepath.Join(s.SessionDir(sessionID), "outputs", id+".log")
}

func (s *Store) ensureSessionDirs(sessionID string) error {
	for _, sub := range []string{"commands", "outputs"} {
		if err := os.MkdirAll(filepath.Join(s.SessionDir(sessionID), sub), 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	return nil
}

// Save writes or overwrites the metadata file atomically (tmp + rename).
// The session's directory is created on demand.
func (s *Store) Save(sessionID string, r Record) error {
	if err := s.ensureSessionDirs(sessionID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	final := s.commandPath(sessionID, r.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (s *Store) Get(sessionID, id string) (Record, error) {
	data, err := os.ReadFile(s.commandPath(sessionID, id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// WriteOutput writes the entire output buffer for a command to its log file.
func (s *Store) WriteOutput(sessionID, id string, body []byte) error {
	if err := s.ensureSessionDirs(sessionID); err != nil {
		return err
	}
	return os.WriteFile(s.outputPath(sessionID, id), body, 0o600)
}

func (s *Store) OutputPath(sessionID, id string) string {
	return s.outputPath(sessionID, id)
}

// ReadOutput reads the output file for a command. Returns (nil, nil) if no
// output file exists yet (command may still be running or never had output).
func (s *Store) ReadOutput(sessionID, id string) ([]byte, error) {
	data, err := os.ReadFile(s.outputPath(sessionID, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// List returns records for the given session sorted by StartedAt descending.
// If before != "", only records strictly older than the one with that ID are
// returned. A session with no commands yet returns (nil, nil), not an error.
func (s *Store) List(sessionID string, limit int, before string) ([]Record, error) {
	dir := filepath.Join(s.SessionDir(sessionID), "commands")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		r, err := s.Get(sessionID, id)
		if err != nil {
			continue
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartedAt.After(all[j].StartedAt)
	})
	if before != "" {
		var beforeRec *Record
		for i := range all {
			if all[i].ID == before {
				beforeRec = &all[i]
				break
			}
		}
		if beforeRec != nil {
			filtered := all[:0]
			for _, r := range all {
				if r.StartedAt.Before(beforeRec.StartedAt) {
					filtered = append(filtered, r)
				}
			}
			all = filtered
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// SweepRunningToInterrupted scans every session and marks any record left
// in the "running" state as interrupted. Called once at boot for sessions
// whose bash is known to be gone (e.g., Pod-restart reconciliation).
//
// Pass an explicit list of sessionIDs to limit the sweep. An empty slice
// sweeps every session whose directory exists under sessions/.
func (s *Store) SweepRunningToInterrupted(sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		// Discover every session directory currently on disk.
		entries, err := os.ReadDir(filepath.Join(s.dir, "sessions"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				sessionIDs = append(sessionIDs, e.Name())
			}
		}
	}
	for _, sid := range sessionIDs {
		all, err := s.List(sid, 0, "")
		if err != nil {
			return fmt.Errorf("list %s: %w", sid, err)
		}
		for _, r := range all {
			if r.Status == StatusRunning {
				r.Status = StatusInterrupted
				if err := s.Save(sid, r); err != nil {
					return fmt.Errorf("sweep %s/%s: %w", sid, r.ID, err)
				}
			}
		}
	}
	return nil
}

// DeleteSession removes the entire session directory (commands + outputs).
// Idempotent: returns nil if the directory is already gone.
func (s *Store) DeleteSession(sessionID string) error {
	err := os.RemoveAll(s.SessionDir(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
```

- [ ] **Step 2: Update store_test.go to pass sessionID to every call**

Rewrite `internal/store/store_test.go`:

```go
package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSession = "sess-1"

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return s
}

func TestStore_SaveAndGet(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := Record{
		ID:        "01HAB",
		SessionID: testSession,
		Command:   "ls",
		Cwd:       "/tmp",
		StartedAt: now,
		Status:    StatusRunning,
	}
	if err := s.Save(testSession, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Get(testSession, "01HAB")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != testSession {
		t.Fatalf("session_id not round-tripped: %q", got.SessionID)
	}
	if got.Command != "ls" || got.Status != StatusRunning {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_SaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	rec := Record{ID: "A", SessionID: testSession, Command: "x", Status: StatusRunning}
	if err := s.Save(testSession, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.SessionDir(testSession), "commands"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(testSession, "missing")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_ListReturnsMostRecentFirst(t *testing.T) {
	s := newTestStore(t)
	for i, id := range []string{"A", "B", "C"} {
		rec := Record{
			ID:        id,
			SessionID: testSession,
			Command:   id,
			Status:    StatusCompleted,
			StartedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := s.Save(testSession, rec); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	list, err := s.List(testSession, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	if list[0].ID != "C" || list[2].ID != "A" {
		t.Fatalf("order wrong: %+v", list)
	}
}

func TestStore_ListRespectsBefore(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"A", "B", "C", "D"} {
		_ = s.Save(testSession, Record{
			ID: id, SessionID: testSession, Command: id,
			Status: StatusCompleted, StartedAt: time.Now().UTC(),
		})
		time.Sleep(10 * time.Millisecond)
	}
	list, err := s.List(testSession, 10, "C")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	gotIDs := make([]string, len(list))
	for i, r := range list {
		gotIDs[i] = r.ID
	}
	if len(list) != 2 || list[0].ID != "B" || list[1].ID != "A" {
		t.Fatalf("got %v, want [B A]", gotIDs)
	}
}

func TestStore_WriteAndReadOutput(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(testSession, Record{
		ID: "X", SessionID: testSession, Command: "x", Status: StatusRunning,
	})
	if err := s.WriteOutput(testSession, "X", []byte("hello\nworld\n")); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	data, err := s.ReadOutput(testSession, "X")
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if string(data) != "hello\nworld\n" {
		t.Fatalf("got %q", data)
	}
}

func TestStore_ReadOutput_MissingFile(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(testSession, Record{
		ID: "X", SessionID: testSession, Command: "x", Status: StatusRunning,
	})
	data, err := s.ReadOutput(testSession, "X")
	if err != nil {
		t.Fatalf("ReadOutput on missing file should not error, got %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil, got %q", data)
	}
}

func TestStore_SweepMarksRunningAsInterrupted(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(testSession, Record{
		ID: "stuck", SessionID: testSession, Status: StatusRunning, Command: "sleep",
	})
	_ = s.Save(testSession, Record{
		ID: "done", SessionID: testSession, Status: StatusCompleted, Command: "ls",
	})
	if err := s.SweepRunningToInterrupted([]string{testSession}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stuck, _ := s.Get(testSession, "stuck")
	if stuck.Status != StatusInterrupted {
		t.Fatalf("stuck status = %s, want interrupted", stuck.Status)
	}
	done, _ := s.Get(testSession, "done")
	if done.Status != StatusCompleted {
		t.Fatalf("done changed unexpectedly: %s", done.Status)
	}
}

func TestStore_ListIsolatedBySession(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save("sess-A", Record{ID: "1", SessionID: "sess-A", Command: "ls", Status: StatusCompleted, StartedAt: time.Now().UTC()})
	_ = s.Save("sess-B", Record{ID: "2", SessionID: "sess-B", Command: "pwd", Status: StatusCompleted, StartedAt: time.Now().UTC()})
	listA, _ := s.List("sess-A", 0, "")
	listB, _ := s.List("sess-B", 0, "")
	if len(listA) != 1 || listA[0].ID != "1" {
		t.Fatalf("sess-A list: %+v", listA)
	}
	if len(listB) != 1 || listB[0].ID != "2" {
		t.Fatalf("sess-B list: %+v", listB)
	}
}

func TestStore_DeleteSession_RemovesAllArtifacts(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(testSession, Record{ID: "1", SessionID: testSession, Command: "ls", Status: StatusCompleted, StartedAt: time.Now().UTC()})
	_ = s.WriteOutput(testSession, "1", []byte("out\n"))
	if err := s.DeleteSession(testSession); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(s.SessionDir(testSession)); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists: %v", err)
	}
	// Idempotent on missing.
	if err := s.DeleteSession(testSession); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestStore_List_UnknownSession_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.List("never-existed", 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list != nil {
		t.Fatalf("expected nil slice, got %+v", list)
	}
}
```

- [ ] **Step 3: Add the SessionDir path-shape test**

Append to `internal/store/store_test.go`:

```go
func TestStore_SessionDir_LayoutIsStable(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	got := s.SessionDir("01HX...")
	want := filepath.Join(dir, "sessions", "01HX...")
	if got != want {
		t.Fatalf("SessionDir = %q, want %q", got, want)
	}
}
```

This is a single line of behavior but guards against silent layout
shifts (e.g., someone "tidies" the prefix from `sessions/` to
`runtime/sessions/`); Plan 4 (Manager) constructs `pty.stream` paths
relative to this — drift here = data loss there.

- [ ] **Step 4: Run all store tests, confirm green**

Run: `go test ./internal/store/ -count=1 -v 2>&1 | grep -c "^--- PASS"`
Expected: 12 (the 8 pre-existing tests now session-scoped + 4 new:
`ListIsolatedBySession`, `DeleteSession_RemovesAllArtifacts`,
`List_UnknownSession_ReturnsEmpty`, `SessionDir_LayoutIsStable`).

If `internal/api` or `cmd/alfred-server` fail to compile here, **leave them broken** — Plan 4 (Manager) and Plan 5 (API) will rewire them. We're verifying store in isolation.

Restrict your test runs to the store package for the rest of this plan: `go test ./internal/store/...`. Do **not** run `go build ./...` until Plan 5 completes.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "store: scope every method by sessionID; add DeleteSession; nest dirs under sessions/<id>/"
```

---

## Task 3: SessionsFile — read/write `sessions.json`

A tiny helper that owns the metadata list. Kept separate from `Store` because it has a different lifecycle (one file, not per-session directories) and is read+written atomically.

**Files:**
- Create: `internal/store/sessions.go`
- Create: `internal/store/sessions_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/store/sessions_test.go`:

```go
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionsFile_LoadMissing_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	list, err := sf.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty, got %+v", list)
	}
}

func TestSessionsFile_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	now := time.Now().UTC().Truncate(time.Second)
	in := []SessionMeta{
		{ID: "a", Name: "Session 1", CreatedAt: now},
		{ID: "b", Name: "training", CreatedAt: now.Add(time.Minute)},
	}
	if err := sf.Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := sf.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out) != 2 || out[0].ID != "a" || out[1].Name != "training" {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestSessionsFile_SaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	if err := sf.Save([]SessionMeta{{ID: "a"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover tmp: %s", e.Name())
		}
	}
}

func TestSessionsFile_LoadMalformed_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	_ = os.WriteFile(filepath.Join(dir, "sessions.json"), []byte("not json"), 0o600)
	_, err := sf.Load()
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail to compile (good — types not defined)**

Run: `go test ./internal/store/ -run TestSessionsFile -count=1`
Expected: BUILD FAILS on `NewSessionsFile`, `SessionMeta` undefined.

- [ ] **Step 3: Implement SessionsFile and SessionMeta**

Create `internal/store/sessions.go`:

```go
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SessionMeta is the persistent metadata for one session. Fields are kept
// minimal on purpose; runtime state (is bash alive, current command) is
// not stored here — that lives in the in-memory session.Manager.
type SessionMeta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionsFile is the persisted list of sessions. The file lives at
// <dir>/sessions.json and is read+written as a whole list under an
// atomic tmp+rename. There is no per-entry locking; all writes flow
// through one goroutine (the session.Manager owns the only writer).
type SessionsFile struct {
	dir string
}

func NewSessionsFile(dir string) *SessionsFile {
	return &SessionsFile{dir: dir}
}

func (sf *SessionsFile) path() string {
	return filepath.Join(sf.dir, "sessions.json")
}

// Load returns the persisted list. Returns (nil, nil) if the file
// doesn't exist yet (first boot, never written).
func (sf *SessionsFile) Load() ([]SessionMeta, error) {
	data, err := os.ReadFile(sf.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []SessionMeta
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Save writes the list atomically. Empty list writes an empty JSON
// array (`[]`), not deletes the file — keeps Load's "missing means
// never written" signal meaningful.
func (sf *SessionsFile) Save(list []SessionMeta) error {
	if list == nil {
		list = []SessionMeta{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	final := sf.path()
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}
```

- [ ] **Step 4: Run tests, confirm green**

Run: `go test ./internal/store/ -run TestSessionsFile -count=1 -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sessions.go internal/store/sessions_test.go
git commit -m "store: add SessionsFile helper for atomic sessions.json read/write"
```

---

## Task 4: MigrateLegacyLayout — detect old flat layout and import

Detect `<data>/commands/` and `<data>/outputs/` from the single-bash era, move every file into a synthetic "Imported" session, and add it to `sessions.json`. Idempotent: a second call after migration is a no-op (no legacy dirs to find).

**Files:**
- Create: `internal/store/migrate.go`
- Create: `internal/store/migrate_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/store/migrate_test.go`:

```go
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrate_NoLegacyDirs_NoOp(t *testing.T) {
	dir := t.TempDir()
	imported, err := MigrateLegacyLayout(dir, "imported-id-A", time.Now().UTC())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if imported {
		t.Fatal("expected imported=false on a fresh dir, got true")
	}
}

func TestMigrate_LegacyDirsExist_FoldsIntoSession(t *testing.T) {
	dir := t.TempDir()
	// Seed the legacy layout.
	seedLegacyRecord(t, dir, "01HZA", `{"id":"01HZA","command":"ls","cwd":"/tmp","started_at":"2026-06-10T10:00:00Z","finished_at":"2026-06-10T10:00:01Z","exit_code":0,"output_truncated":false,"status":"completed"}`)
	seedLegacyRecord(t, dir, "01HZB", `{"id":"01HZB","command":"pwd","cwd":"/tmp","started_at":"2026-06-10T10:01:00Z","finished_at":"2026-06-10T10:01:01Z","exit_code":0,"output_truncated":false,"status":"completed"}`)
	seedLegacyOutput(t, dir, "01HZA", "tmp foo\n")
	seedLegacyOutput(t, dir, "01HZB", "/tmp\n")

	now := time.Now().UTC().Truncate(time.Second)
	imported, err := MigrateLegacyLayout(dir, "imp-1", now)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !imported {
		t.Fatal("expected imported=true")
	}

	// New layout: every command JSON moved + has session_id stamped.
	s, _ := New(dir)
	recA, err := s.Get("imp-1", "01HZA")
	if err != nil {
		t.Fatalf("Get 01HZA: %v", err)
	}
	if recA.SessionID != "imp-1" {
		t.Fatalf("session_id not stamped: %+v", recA)
	}
	if recA.Command != "ls" {
		t.Fatalf("command not preserved: %q", recA.Command)
	}
	outA, _ := s.ReadOutput("imp-1", "01HZA")
	if string(outA) != "tmp foo\n" {
		t.Fatalf("output not migrated: %q", outA)
	}

	// sessions.json has the imported entry.
	sf := NewSessionsFile(dir)
	list, _ := sf.Load()
	if len(list) != 1 || list[0].ID != "imp-1" || list[0].Name != "Imported" {
		t.Fatalf("sessions.json wrong: %+v", list)
	}

	// Legacy dirs are gone.
	if _, err := os.Stat(filepath.Join(dir, "commands")); !os.IsNotExist(err) {
		t.Fatalf("legacy commands/ still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outputs")); !os.IsNotExist(err) {
		t.Fatalf("legacy outputs/ still exists: %v", err)
	}
}

func TestMigrate_AlreadyMigrated_NoOp(t *testing.T) {
	dir := t.TempDir()
	// Pre-existing sessions.json signals "we've been here before".
	sf := NewSessionsFile(dir)
	_ = sf.Save([]SessionMeta{{ID: "pre", Name: "Existing", CreatedAt: time.Now().UTC()}})
	// Even with legacy dirs sitting around, migration shouldn't run.
	seedLegacyRecord(t, dir, "01HZX", `{"id":"01HZX","command":"ls"}`)
	imported, err := MigrateLegacyLayout(dir, "should-not-be-used", time.Now().UTC())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if imported {
		t.Fatal("expected imported=false when sessions.json already present")
	}
}

func TestMigrate_MalformedLegacyJSON_Skipped(t *testing.T) {
	dir := t.TempDir()
	seedLegacyRecord(t, dir, "01HZG", `{"id":"01HZG","command":"ok","status":"completed","started_at":"2026-06-10T10:00:00Z"}`)
	seedLegacyRecord(t, dir, "01HZB", `not json`) // malformed
	_, err := MigrateLegacyLayout(dir, "imp", time.Now().UTC())
	if err != nil {
		t.Fatalf("migrate should not fail on one malformed record: %v", err)
	}
	s, _ := New(dir)
	// The good record made it through.
	rec, err := s.Get("imp", "01HZG")
	if err != nil {
		t.Fatalf("Get good: %v", err)
	}
	if rec.Command != "ok" {
		t.Fatalf("good record corrupted: %+v", rec)
	}
	// The malformed one was skipped (Get returns NotFound).
	if _, err := s.Get("imp", "01HZB"); err != ErrNotFound {
		t.Fatalf("malformed record should be skipped, got %v", err)
	}
}

func seedLegacyRecord(t *testing.T, dir, id, jsonBody string) {
	t.Helper()
	// Sanity-check the fixture: legacy records were valid JSON in the
	// pre-multi-session schema. We accept malformed bytes too (for the
	// "skip bad records" test), so this only validates when the fixture
	// LOOKS like JSON (starts with '{').
	if len(jsonBody) > 0 && jsonBody[0] == '{' {
		var probe map[string]any
		if err := json.Unmarshal([]byte(jsonBody), &probe); err != nil {
			t.Fatalf("fixture is not valid JSON: %v", err)
		}
	}
	cmdsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(cmdsDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cmdsDir, id+".json"), []byte(jsonBody), 0o600); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
}

func seedLegacyOutput(t *testing.T, dir, id, body string) {
	t.Helper()
	outDir := filepath.Join(dir, "outputs")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy outputs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, id+".log"), []byte(body), 0o600); err != nil {
		t.Fatalf("write legacy output: %v", err)
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail to compile**

Run: `go test ./internal/store/ -run TestMigrate -count=1`
Expected: BUILD FAILS on `MigrateLegacyLayout` undefined.

- [ ] **Step 3: Implement MigrateLegacyLayout**

Create `internal/store/migrate.go`:

```go
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MigrateLegacyLayout detects the pre-multi-session layout
// (<dir>/commands/*.json + <dir>/outputs/*.log) and folds every
// command into a single new session with the given importedSessionID
// and name "Imported".
//
// Returns (imported=true, nil) when migration ran.
// Returns (imported=false, nil) when there was nothing to migrate
// (sessions.json already exists, or there are no legacy dirs).
//
// The detection guard is "sessions.json doesn't exist yet" — once
// we've ever written sessions.json, this is a no-op even if stale
// legacy dirs happen to remain.
func MigrateLegacyLayout(dir, importedSessionID string, createdAt time.Time) (bool, error) {
	sf := NewSessionsFile(dir)
	existing, err := sf.Load()
	if err != nil {
		return false, fmt.Errorf("load sessions.json: %w", err)
	}
	if existing != nil {
		// Already initialized; skip.
		return false, nil
	}

	legacyCmds := filepath.Join(dir, "commands")
	legacyOuts := filepath.Join(dir, "outputs")
	if _, err := os.Stat(legacyCmds); errors.Is(err, os.ErrNotExist) {
		// Fresh install. Nothing to migrate, nothing to write — caller
		// will Save() sessions.json once a real session is created.
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("stat legacy commands: %w", err)
	}

	store, err := New(dir)
	if err != nil {
		return false, fmt.Errorf("new store: %w", err)
	}

	entries, err := os.ReadDir(legacyCmds)
	if err != nil {
		return false, fmt.Errorf("readdir legacy commands: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		raw, err := os.ReadFile(filepath.Join(legacyCmds, e.Name()))
		if err != nil {
			// Surface I/O errors; skip parse errors silently.
			return false, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			// Stamp this skip in stderr so the operator notices.
			fmt.Fprintf(os.Stderr, "migrate: skipping malformed %s: %v\n", e.Name(), err)
			continue
		}
		rec.SessionID = importedSessionID
		if err := store.Save(importedSessionID, rec); err != nil {
			return false, fmt.Errorf("save migrated %s: %w", id, err)
		}
		legacyOut := filepath.Join(legacyOuts, id+".log")
		if data, err := os.ReadFile(legacyOut); err == nil {
			if err := store.WriteOutput(importedSessionID, id, data); err != nil {
				return false, fmt.Errorf("migrate output %s: %w", id, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("read legacy output %s: %w", id, err)
		}
	}

	// Persist the synthetic session entry.
	meta := SessionMeta{
		ID:        importedSessionID,
		Name:      "Imported",
		CreatedAt: createdAt,
	}
	if err := sf.Save([]SessionMeta{meta}); err != nil {
		return false, fmt.Errorf("save sessions.json: %w", err)
	}

	// Remove the legacy dirs only after a successful save.
	if err := os.RemoveAll(legacyCmds); err != nil {
		return false, fmt.Errorf("remove legacy commands: %w", err)
	}
	if err := os.RemoveAll(legacyOuts); err != nil {
		return false, fmt.Errorf("remove legacy outputs: %w", err)
	}
	return true, nil
}
```

- [ ] **Step 4: Run tests, confirm green**

Run: `go test ./internal/store/ -run TestMigrate -count=1 -v`
Expected: 4 PASS.

- [ ] **Step 5: Run full store package test, confirm everything still green**

Run: `go test ./internal/store/ -count=1 -v 2>&1 | grep -c "^=== RUN"`
Expected exactly 20 tests:
- 12 from Task 2 (8 carried over + 4 new: `ListIsolatedBySession`, `DeleteSession_RemovesAllArtifacts`, `List_UnknownSession_ReturnsEmpty`, `SessionDir_LayoutIsStable`)
- 4 from Task 3 SessionsFile (`LoadMissing_ReturnsEmpty`, `SaveAndLoad`, `SaveIsAtomic`, `LoadMalformed_ReturnsError`)
- 4 from this task (`Migrate_NoLegacyDirs_NoOp`, `Migrate_LegacyDirsExist_FoldsIntoSession`, `Migrate_AlreadyMigrated_NoOp`, `Migrate_MalformedLegacyJSON_Skipped`)

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrate.go internal/store/migrate_test.go
git commit -m "store: add MigrateLegacyLayout for one-shot import of pre-multi-session data"
```

---

## Plan 1 acceptance

At the end of Plan 1:

- `go test -race -count=1 ./internal/store/` passes (20 tests).
- `internal/store` no longer talks to a single command directory — every public method takes a `sessionID`.
- `Record.SessionID` is part of the JSON schema.
- `SessionsFile.Save/Load` round-trips `sessions.json` atomically.
- `MigrateLegacyLayout(dir, id, time)` is idempotent and skips malformed records with a stderr line.

### Migration durability — accepted limitations

`MigrateLegacyLayout` is **not** fully transactional. The sequence is:
1. Copy every command JSON + output into the new per-session layout.
2. Write `sessions.json` with the "Imported" entry.
3. `RemoveAll` the legacy `commands/` and `outputs/` directories.

A crash between step 2 and step 3 leaves `sessions.json` written + legacy dirs still present. On next boot the migration **skips** (because `sessions.json` exists) and the legacy dirs sit there orphaned, harming nothing. A crash between step 1 and step 2 wastes some disk on duplicated data; next boot re-migrates from scratch. A crash mid-step-1 (writing one command JSON) leaves a half-migrated layout that subsequent boot completes.

This is acceptable for a single-user tool that runs migration exactly once per upgrade. We document the limitation here rather than building a real two-phase commit on top of a Linux filesystem.

The rest of the codebase **does not compile** at the end of Plan 1, because callers of the old store API (`internal/api/commands.go`, `internal/api/ws.go`, etc.) still use the old signatures. That's expected — Plan 5 (REST) and Plan 6 (WS) rewire them. Other plans (2, 3, 4) work on `internal/shell` and `internal/session` and don't touch `internal/store` callers either.

If you want a green `go build ./...` before moving on, the smallest stopgap is to drop a `// TODO(plan-5): adapt to per-session store` line and `_ = sessionID` hack in `internal/api/commands.go` — but that's not part of this plan's deliverable.

---

## Plan 1 self-review checklist

Before moving to Plan 2, the engineer should verify:

- [ ] No `TODO`, `FIXME`, or panic markers in the new files (`grep -rE "TODO|FIXME|panic\(" internal/store/`).
- [ ] The new files compile in isolation (`go vet ./internal/store/`).
- [ ] `go test -race -count=1 ./internal/store/` is green.
- [ ] `git log --oneline` shows 4 commits, one per task.
- [ ] `grep "Save(" internal/store/store.go internal/store/*_test.go | grep -v "sessionID\|testSession\|sf\\.Save"` returns empty — no legacy single-arg Save lingering.
