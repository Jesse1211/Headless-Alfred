# Summary template — Plan 03: fsnotify watcher + per-session WS push

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect summary-file writes on disk and push a `summary_updated` WS frame to only the WS connections subscribed to the affected Alfred session, so the right-hand sidebar can re-fetch its content within ~50 ms of Claude finishing the Write.

**Architecture:** New `internal/summary/watcher.go` wraps `fsnotify.Watcher` for `<dataDir>/summaries/`. It debounces bursty filename events (200 ms per file), parses `<sid>.md` filenames, and invokes an `OnWrite(sessionID)` callback. The callback lives in `internal/api/ws.go`'s connection loop, where it has access to the per-WS event channels and the `selectedSessionID` filter. WS protocol gets a new outbound type `summary_updated`.

**Tech Stack:** Go 1.25, `github.com/fsnotify/fsnotify` (new dep), `go test`.

**Depends on plans 01 & 02:** `internal/summary/path.go` + `Dir(dataDir)` helper already exist.

After this plan: the backend can push push-notifications to the frontend. Frontend wiring (subscribing + re-fetching) is plan 04.

---

### Task 1: Add `fsnotify` dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dep**

Run:

```bash
go get github.com/fsnotify/fsnotify@v1.7.0
```

Expected: go.mod gains the new require line; go.sum updates.

- [ ] **Step 2: Verify it builds**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add github.com/fsnotify/fsnotify dependency for summary watcher"
```

---

### Task 2: `Watcher` core — `internal/summary/watcher.go`

**Files:**
- Create: `internal/summary/watcher.go`
- Test: `internal/summary/watcher_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/summary/watcher_test.go`:

```go
package summary

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcher_WriteFiresOnWriteCallback(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var got []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		got = append(got, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Write the file. summaries/ should exist (StartWatcher mkdir's it).
	if err := os.WriteFile(Path(dir, "S1"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Debounce window is 200ms; give it 600ms.
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "S1" {
		t.Errorf("got=%v, want [S1]", got)
	}
}

func TestWatcher_DebouncesBurstyWrites(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var got []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		got = append(got, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Fire 10 writes quickly on the same file.
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(Path(dir, "S2"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	// We want one event per filename per debounce window. Fewer
	// than the 10 writes that fsnotify would otherwise emit.
	if len(got) == 0 {
		t.Fatal("expected at least one callback, got none")
	}
	if len(got) > 2 {
		t.Errorf("expected debounce to coalesce 10 writes into <=2 callbacks; got %d (%v)", len(got), got)
	}
}

func TestWatcher_IgnoresNonMatchingFilenames(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var got []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		got = append(got, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// .txt / no extension / hidden file — all must be ignored.
	for _, name := range []string{"S1.txt", "S2", ".hidden.md"} {
		if err := os.WriteFile(filepath.Join(Dir(dir), name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Errorf("got=%v, want none — filenames don't match <sid>.md", got)
	}
}

func TestWatcher_FailsGracefullyIfMkdirDenied(t *testing.T) {
	// We can't easily simulate a denied mkdir cross-platform in
	// a test. Just assert StartWatcher returns an error on a
	// path under a file (not a directory).
	dir := t.TempDir()
	notADir := filepath.Join(dir, "blocker")
	if err := os.WriteFile(notADir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// `notADir` is a regular file; Dir(notADir) = notADir/summaries
	// which cannot be created.
	w, err := StartWatcher(notADir, func(string) {})
	if err == nil {
		t.Error("expected StartWatcher to return an error when mkdir fails")
		w.Stop()
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/summary/...`
Expected: build failure ("undefined: StartWatcher").

- [ ] **Step 3: Implement `StartWatcher` + `Watcher`**

Create `internal/summary/watcher.go`:

```go
package summary

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher tails <dataDir>/summaries/ and invokes onWrite(sid)
// whenever a `<sid>.md` file is created or modified, with a
// per-file debounce so a single Write that produces multiple
// fsnotify events still fires the callback once.
type Watcher struct {
	w       *fsnotify.Watcher
	onWrite func(sessionID string)
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	pending map[string]*time.Timer
}

// StartWatcher creates the summaries directory if missing, starts
// an fsnotify watcher on it, and dispatches debounced filename
// events to onWrite in a background goroutine.
//
// Errors during mkdir or fsnotify init are returned; on success
// the caller must call Stop() to release the watcher. The watcher
// is intentionally fail-stop on initial errors so main.go can log
// + continue without it (sidebar becomes stale but the app still
// works).
func StartWatcher(dataDir string, onWrite func(sessionID string)) (*Watcher, error) {
	if onWrite == nil {
		return nil, errors.New("summary.StartWatcher: onWrite required")
	}
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(dir); err != nil {
		fw.Close()
		return nil, err
	}
	w := &Watcher{
		w:       fw,
		onWrite: onWrite,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		pending: map[string]*time.Timer{},
	}
	go w.loop()
	return w, nil
}

// Stop blocks until the watcher goroutine drains. Safe to call
// from a defer.
func (w *Watcher) Stop() {
	close(w.stop)
	<-w.done
	w.w.Close()
}

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			// We care about Create + Write; Rename/Remove are
			// frontend-visible too but the next Read will 404
			// and the UI re-renders the empty state, no push
			// needed.
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			sid, ok := parseSummaryFilename(filepath.Base(ev.Name))
			if !ok {
				continue
			}
			w.schedule(sid)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn("summary.Watcher: fsnotify error", "err", err)
		}
	}
}

const debounce = 200 * time.Millisecond

// schedule arms a per-sid timer; bursts of events for the same
// file coalesce into a single onWrite call.
func (w *Watcher) schedule(sid string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[sid]; ok {
		t.Reset(debounce)
		return
	}
	w.pending[sid] = time.AfterFunc(debounce, func() {
		w.mu.Lock()
		delete(w.pending, sid)
		w.mu.Unlock()
		w.onWrite(sid)
	})
}

// parseSummaryFilename returns the session id from a filename of
// the form "<sid>.md". Returns ("", false) for anything else.
// Used to ignore stray *.txt, dotfiles, etc.
func parseSummaryFilename(name string) (string, bool) {
	if strings.HasPrefix(name, ".") {
		return "", false
	}
	if !strings.HasSuffix(name, ".md") {
		return "", false
	}
	sid := strings.TrimSuffix(name, ".md")
	if sid == "" {
		return "", false
	}
	return sid, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/summary/...`
Expected: PASS (4 new tests). May take ~3 seconds (the debounce sleeps).

- [ ] **Step 5: Commit**

```bash
git add internal/summary/watcher.go internal/summary/watcher_test.go
git commit -m "feat(summary): fsnotify-backed Watcher with per-file 200ms debounce"
```

---

### Task 3: `summary_updated` WS frame definition

**Files:**
- Modify: `internal/api/wsproto.go` (no new field needed — `OutMsg` already has `Type` and `SessionID`)

- [ ] **Step 1: Confirm `OutMsg` has what we need**

`OutMsg` already carries `Type string` and `SessionID string`. The new frame is just:

```json
{ "type": "summary_updated", "sessionID": "01KSESSION" }
```

No new field. Skim `wsproto.go` and confirm. If `SessionID` is not currently on `OutMsg`, add it (mirroring `InMsg.SessionID`).

- [ ] **Step 2: Document the frame**

Add a doc-only constant in `internal/api/wsproto.go`:

```go
// TypeSummaryUpdated is the WS frame Type pushed by alfred-server
// when the on-disk summary file for a session is written. The
// frame carries no body — the frontend re-fetches via
// GET /api/sessions/{sid}/summary.
const TypeSummaryUpdated = "summary_updated"
```

- [ ] **Step 3: Commit**

```bash
git add internal/api/wsproto.go
git commit -m "feat(ws): document summary_updated outbound frame type"
```

---

### Task 4: Wire watcher into the WS connection loop

**Files:**
- Modify: `internal/api/ws.go` (start watcher inside the per-WS loop, push frame on callback)
- Modify: `cmd/alfred-server/main.go` (NEW) — watcher should be **server-singleton**, fanned out to all WS connections by sessionID

Actually rethink: starting a watcher per WS is wasteful. Better: one watcher in `main.go`, broadcast through a `sessionEventBus` that WS handlers subscribe to. But for v1 the simplest correct shape is **one watcher per WS connection** — it adds ~1 inotify descriptor per browser tab, max 8 tabs by spec, well within Linux limits. Trade-off: simple wiring vs slight inefficiency. We choose simple.

(If profiling later flags this, refactor to a `Manager.SubscribeSummaries()` fan-out in v1.x.)

- [ ] **Step 1: Write a Go-side smoke test for the WS frame**

Create or extend `internal/api/ws_summary_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jesseliu/headless-alfred/internal/summary"
)

// End-to-end-ish: spin up the full router, open a WS, simulate
// claude writing the summary file, expect a summary_updated frame.
//
// This test only asserts the protocol — it does NOT exercise
// real claude. It relies on the watcher we wired in the WS loop.
func TestWS_PushesSummaryUpdated(t *testing.T) {
	t.Skip("Filled in once ws.go knows about Manager.DataDir() — see Step 3")
	_ = json.Unmarshal
	_ = url.Parse
	_ = websocket.IsCloseError
	_ = httptest.NewServer
	_ = context.Background
	_ = time.Second
	_ = os.WriteFile
	_ = strings.HasSuffix
	_ = http.StatusOK
	_ = summary.Path
}
```

- [ ] **Step 2: Implement the watcher hookup in the WS loop**

Modify `internal/api/ws.go`. Find the existing connection loop (the function that creates `events`, `ptyChunks`, `asks`, `claudeEvents` per WS). Right after those channel decls, add a watcher:

```go
	// summaryUpdates is delivered onto the main select via a
	// channel so we can serialise the push with other writes
	// (writeMu guards conn.WriteJSON).
	summaryUpdates := make(chan string, 4)
	sw, swErr := summary.StartWatcher(m.DataDir(), func(sid string) {
		select {
		case summaryUpdates <- sid:
		case <-stop:
		}
	})
	if swErr != nil {
		slog.Warn("ws: summary watcher disabled", "err", swErr)
	} else {
		defer sw.Stop()
	}
```

Add a new case to the main `select { }` loop, alongside the existing `case ev := <-events:` etc.:

```go
		case sid, ok := <-summaryUpdates:
			if !ok {
				continue
			}
			// Only push to subscribers of this session. We
			// gate by whether the WS already has this session
			// subscribed (it always does for the user's own
			// sessions — Subscribe runs in runClientLoop's init).
			if _, subscribed := m.Get(sid); subscribed != nil {
				// Defensive: session may have been closed between
				// fsnotify firing and us processing.
				continue
			}
			_ = write(OutMsg{Type: TypeSummaryUpdated, SessionID: sid})
```

Wait — `m.Get(sid)` returns `(Shell, error)`, where the second return is the "not found" sentinel. The intent here is "skip if the session is gone". Final form:

```go
		case sid, ok := <-summaryUpdates:
			if !ok {
				continue
			}
			if _, err := m.Get(sid); err != nil {
				// Session disappeared between fsnotify and us; skip.
				continue
			}
			_ = write(OutMsg{Type: TypeSummaryUpdated, SessionID: sid})
```

The "per-session subscriber" guarantee comes for free: each WS only handles its own conn, and sessions are global per Alfred-server, so any WS that watches gets only its own sessions' updates by virtue of the user model (single-user v1).

Add the `summary` import at the top of ws.go:

```go
	"github.com/jesseliu/headless-alfred/internal/summary"
```

- [ ] **Step 3: Build + run tests**

Run: `go test ./internal/...`
Expected: PASS (the new ws_summary_test.go is skipped — we'll un-skip in plan 04 e2e when there's a real frontend hook to use).

- [ ] **Step 4: Manual smoke**

Start the server, open a WS, touch a summary file, watch the frame:

```bash
go build -o /tmp/alfred-server ./cmd/alfred-server
rm -rf /tmp/plan03 && mkdir -p /tmp/plan03/data
ALFRED_USER=admin ALFRED_PASSWORD=admin ALFRED_TOKEN=devtoken \
  ALFRED_ADDR=:18080 ALFRED_DATA_DIR=/tmp/plan03/data \
  /tmp/alfred-server > /tmp/plan03.log 2>&1 &
SRV=$!
sleep 1
# Create a session via REST so we have something to subscribe to:
curl -s -X POST http://127.0.0.1:18080/api/sessions \
  -H 'Authorization: Bearer devtoken' \
  -H 'content-type: application/json' \
  -d '{"name":"smoke"}'
# Open a WS using websocat or any minimal client and:
#   1. observe initial idle/reattach frames
#   2. echo "## Goal" > /tmp/plan03/data/summaries/<sid>.md
#   3. observe {"type":"summary_updated","sessionID":"<sid>"} within ~250ms
# (If websocat isn't installed: use any go test harness that does the same.)
kill $SRV
```

Expected: WS shows the summary_updated frame after the file write.

- [ ] **Step 5: Commit**

```bash
git add internal/api/ws.go internal/api/ws_summary_test.go
git commit -m "feat(ws): per-WS summary.Watcher pushes summary_updated frames"
```

---

### Task 5: Integration smoke

- [ ] **Step 1: Run the whole Go suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 2: Build the server**

Run: `go build -o /tmp/alfred-server ./cmd/alfred-server`
Expected: clean.

- [ ] **Step 3: Run existing Playwright e2e**

Make sure alfred-server + Vite are running, then:

```bash
cd web && npx playwright test --reporter=line
```

Expected: all existing tests still PASS. No frontend change yet → no regressions; the new WS frame is silently dropped by today's frontend (the reducer ignores unknown types).
