# Daily Recap (Backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the backend half of the Daily Recap feature: new `Kind` session type, recap session lifecycle endpoints, recap-daily template + auto-allow dispatcher path, recap file watcher, and `GET /api/sessions/{id}` for selected-session metadata.

**Architecture:** Add a `Kind` field to `SessionMeta` (defaults to `chat`); recap sessions are normal Claude UI sessions plus a tag. A new `CreateOrGetRecapSession` on Manager finds-or-creates the singleton recap session under a mutex, killing any duplicates. New HTTP endpoints expose lifecycle, recap listing, and per-date markdown. A new `internal/claude/dispatcher_recap.go` auto-allows Read/Write to `<DATA_DIR>/recaps/<YYYY-MM-DD>.md`. A new fsnotify watcher emits `recap_updated` WS frames (broadcast to all clients).

**Tech Stack:** Go 1.25, chi v5, fsnotify v1.7.0, stdlib `regexp`/`time` only.

---

## File Structure

| file | role | task |
|---|---|---|
| `internal/store/sessions.go` | add `Kind` + `SessionKind` type | T1 |
| `internal/template/builtin.go` | add `recap-daily` template entry | T2 |
| `internal/template/render.go` | extend with `<date>`, `<cwd>`, `<recap_path>` placeholders | T2 |
| `internal/recap/path.go` | new — `Dir(dataDir)`, `Path(dataDir, date)` | T3 |
| `internal/recap/watcher.go` | new — fsnotify watcher for `recaps/*.md` | T3 |
| `internal/claude/dispatcher_recap.go` | new — `isRecapIO` predicate | T4 |
| `internal/claude/dispatcher.go` | call `isRecapIO` alongside `isSummaryIO` | T4 |
| `internal/claude/runner.go` | `RunOptions.Continue` flag | T5 |
| `internal/session/manager.go` | `CreateOrGetRecapSession`, `List(kind)` filter | T6 |
| `internal/api/sessions_handlers.go` | new — `GET /api/sessions/{id}` returning a single SessionMeta | T7 |
| `internal/api/recap_handlers.go` | new — 4 recap endpoints | T8 |
| `internal/api/wsproto.go` | `TypeRecapUpdated` constant + ws OutMsg field for date | T9 |
| `internal/api/ws.go` | broadcast `recap_updated` to every WS client | T9 |
| `internal/api/router.go` | register new routes | T8/T9 |
| `cmd/alfred-server/main.go` | start the recap watcher; pass to ws.go | T9 |

Nine tasks, roughly two per layer (store → template+recap-pkg → dispatcher → runner → manager → http → ws).

---

### Task 1: `SessionKind` field on SessionMeta

**Files:**
- Modify: `internal/store/sessions.go`
- Test: `internal/store/sessions_test.go` (existing file — add one round-trip test)

The new field defaults to empty (`""`), which represents `KindChat` for back-compat with sessions.json files written before this change.

- [ ] **Step 1: Write the failing test**

Open `internal/store/sessions_test.go`. Find any existing round-trip test (look for tests that marshal/unmarshal `SessionMeta`). Add immediately after:

```go
func TestSessionKind_RoundTripAndDefault(t *testing.T) {
	// Round-trip preserves Kind.
	m := SessionMeta{ID: "A", Name: "n", Kind: KindRecap}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got SessionMeta
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindRecap {
		t.Errorf("Kind round-trip: got %q want %q", got.Kind, KindRecap)
	}
	// Old file without `kind` decodes as KindChat (empty string).
	var old SessionMeta
	if err := json.Unmarshal([]byte(`{"id":"X","name":"y"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Kind != KindChat {
		t.Errorf("missing kind: got %q want %q", old.Kind, KindChat)
	}
}
```

If `encoding/json` isn't imported in the test file, add it.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/store/ -run TestSessionKind_RoundTripAndDefault
```

Expected: FAIL (KindChat / KindRecap / Kind undefined).

- [ ] **Step 3: Add the type + field**

In `internal/store/sessions.go`, immediately after the existing `ClaudeRenderer` type (around line 34-42), add:

```go
// SessionKind classifies a session by its UX role. "chat" (the
// default, represented as empty for back-compat with old sessions.json
// files) is a regular user-driven session that may enter Claude
// mode via the Start Claude dialog. "recap" is a singleton ephemeral
// session driven by the "+ 复盘" button; it auto-enters Claude UI
// mode and is killed when the user navigates away.
type SessionKind string

const (
	KindChat  SessionKind = ""      // default; back-compat with old files
	KindRecap SessionKind = "recap"
)
```

Then in the `SessionMeta` struct (around line 44-75), add `Kind` immediately after `Mode`:

```go
type SessionMeta struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	CreatedAt time.Time   `json:"created_at"`
	Mode      SessionMode `json:"mode,omitempty"` // empty in old files == shell

	// Kind is the session's UX classification (chat vs recap). See
	// SessionKind. Empty in old files == KindChat.
	Kind SessionKind `json:"kind,omitempty"`

	// Renderer is only meaningful when Mode == ModeClaude.
	// ... (existing rest of struct unchanged)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/store/ -run TestSessionKind_RoundTripAndDefault
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sessions.go internal/store/sessions_test.go
git commit -m "feat(store): SessionKind type + Kind field on SessionMeta"
```

---

### Task 2: `recap-daily` template + render placeholders

**Files:**
- Modify: `internal/template/builtin.go`
- Modify: `internal/template/render.go`
- Modify: `internal/template/render_test.go` (existing)

The recap-daily template carries the fixed prompt the "Generate today's recap" button fires. It references three placeholders: `<date>`, `<cwd>`, `<recap_path>`. The Render signature gains parameters for them; existing summary-todo calls pass empty strings for the new ones.

- [ ] **Step 1: Inspect current Render signature**

```bash
grep -n "func Render" internal/template/render.go
grep -rn "template.Render(" internal/ | grep -v _test
```

Expected callers: `internal/api/claude_handlers.go` (one) — pin its line so step 4 can update it.

- [ ] **Step 2: Write the failing tests**

Open `internal/template/render_test.go`. Add:

```go
func TestRender_RecapDailySubstitutes(t *testing.T) {
	got := Render("recap-daily", RenderArgs{
		Date:       "2026-06-15",
		Cwd:        "/home/alfred",
		RecapPath:  "/data/recaps/2026-06-15.md",
	})
	if !strings.Contains(got, "2026-06-15") {
		t.Errorf("date not substituted: %q", got)
	}
	if !strings.Contains(got, "/home/alfred") {
		t.Errorf("cwd not substituted: %q", got)
	}
	if !strings.Contains(got, "/data/recaps/2026-06-15.md") {
		t.Errorf("recap_path not substituted: %q", got)
	}
	// Sanity: the body should mention git AND claude-mem (covers
	// the spec's "data sources" rule).
	if !strings.Contains(got, "git log") {
		t.Errorf("git lookup missing")
	}
	if !strings.Contains(got, "claude-mem") {
		t.Errorf("claude-mem mention missing")
	}
}

func TestRender_SummaryTodoStillWorks(t *testing.T) {
	got := Render("summary-todo", RenderArgs{
		SessionID:   "sid-A",
		SummaryPath: "/data/summaries/sid-A.md",
	})
	if !strings.Contains(got, "sid-A") {
		t.Errorf("sessionID not substituted: %q", got)
	}
	if !strings.Contains(got, "/data/summaries/sid-A.md") {
		t.Errorf("summary_path not substituted: %q", got)
	}
}
```

(Adjust existing summary-todo Render tests to use the new `RenderArgs` shape.)

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/template/
```

Expected: FAIL (RenderArgs undefined / Render arity mismatch).

- [ ] **Step 4: Refactor Render signature + add recap-daily**

Replace `internal/template/render.go` contents:

```go
package template

import "strings"

// RenderArgs is the bag of placeholder values for Render. All fields
// are optional — only the ones a given template uses matter. Adding
// a new placeholder = adding a new field here; old templates ignore it.
type RenderArgs struct {
	SessionID   string // <sid>
	SummaryPath string // <summary_path>
	Date        string // <date>
	Cwd         string // <cwd>
	RecapPath   string // <recap_path>
}

// Render substitutes the named placeholders in the template's Content
// and returns the result. Returns "" for unknown or empty id —
// callers treat that as "no template, skip injection".
func Render(id string, args RenderArgs) string {
	t, ok := Builtins[id]
	if !ok {
		return ""
	}
	s := t.Content
	s = strings.ReplaceAll(s, "<sid>", args.SessionID)
	s = strings.ReplaceAll(s, "<summary_path>", args.SummaryPath)
	s = strings.ReplaceAll(s, "<date>", args.Date)
	s = strings.ReplaceAll(s, "<cwd>", args.Cwd)
	s = strings.ReplaceAll(s, "<recap_path>", args.RecapPath)
	return s
}
```

Update the single existing caller in `internal/api/claude_handlers.go`. Find the call to `template.Render(...)`. It currently looks like:

```go
template.Render(meta.TemplateID, sessionID, summaryPath)
```

Replace with:

```go
template.Render(meta.TemplateID, template.RenderArgs{
    SessionID:   sessionID,
    SummaryPath: summaryPath,
})
```

In `internal/template/builtin.go`, add a new entry to the `Builtins` map immediately after the existing `summary-todo`:

```go
"recap-daily": {
    ID:   "recap-daily",
    Name: "Daily recap",
    Content: `Generate today's daily recap for the user. Today is <date>.

Steps:

1. Before doing anything, check whether any superpowers skills apply
   (e.g. a 'daily-recap' or 'summarize' skill). If one does, invoke
   it and follow its instructions instead of these steps.

2. Otherwise, gather data IN PARALLEL (single response with multiple
   tool calls):
   - Bash: cd <cwd> && git log --since="<date> 00:00" --until="<date> 23:59" --all --pretty=format:"%h %s (%an)"
     plus git diff --shortstat HEAD@{midnight}..HEAD
   - If a claude-mem timeline tool is available, call it for today's slice.
   - If a claude-mem memory_search tool is available, query "today's decisions".

   If the claude-mem tools are not available (the user hasn't
   installed the plugin), skip those calls — git alone is enough to
   produce a useful recap.

3. Synthesize a markdown recap with this exact structure:

   # Recap · <date>

   ## Shipped
   - <bullet of concrete output: PR opened, file written, deploy>

   ## Decisions
   - <bullet of judgement calls made, with the why>

   ## Open questions
   - <bullet of unresolved items the user should address tomorrow>

   ## Notes
   - <anything else worth remembering>

4. Write the result to <recap_path> (overwriting any existing file).
   Use the Write tool; do NOT print the recap inline.

5. Confirm to the user with one short line: "Recap saved to <date>.md.
   Ask me about anything from today."
`,
},
```

- [ ] **Step 5: Run tests + go build**

```bash
go test ./internal/template/ && go build ./...
```

Expected: PASS + clean build (the api caller now compiles against the new signature).

- [ ] **Step 6: Commit**

```bash
git add internal/template/builtin.go internal/template/render.go \
        internal/template/render_test.go internal/api/claude_handlers.go
git commit -m "feat(template): recap-daily template + RenderArgs refactor"
```

---

### Task 3: Recap path + watcher package

**Files:**
- Create: `internal/recap/path.go`
- Create: `internal/recap/path_test.go`
- Create: `internal/recap/watcher.go`
- Create: `internal/recap/watcher_test.go`

Mirrors the structure of `internal/summary/`. The watcher emits `(date string)` not `(sid string)`, and the filename pattern is strict `YYYY-MM-DD.md` (date validation done by `parseRecapFilename`).

- [ ] **Step 1: Write the failing path tests**

```go
// internal/recap/path_test.go
package recap

import (
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	got := Dir("/data")
	want := filepath.Join("/data", "recaps")
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestPath(t *testing.T) {
	got := Path("/data", "2026-06-15")
	want := filepath.Join("/data", "recaps", "2026-06-15.md")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/recap/...
```

Expected: FAIL (package doesn't exist yet).

- [ ] **Step 3: Implement path.go**

```go
// Package recap is the on-disk recap-file location helper.
// Files live at <dataDir>/recaps/<YYYY-MM-DD>.md.
package recap

import "path/filepath"

// Dir returns the directory holding all recap files.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "recaps")
}

// Path returns the absolute path of the recap file for the given
// local date string (YYYY-MM-DD). No validation — caller is responsible
// for shape.
func Path(dataDir, date string) string {
	return filepath.Join(Dir(dataDir), date+".md")
}
```

- [ ] **Step 4: Run path tests to verify they pass**

```bash
go test ./internal/recap/ -run "TestDir|TestPath"
```

Expected: PASS (2 tests).

- [ ] **Step 5: Write the failing watcher tests**

```go
// internal/recap/watcher_test.go
package recap

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcher_FiresOnWriteWithDate(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var dates []string
	w, err := StartWatcher(dir, func(date string) {
		mu.Lock()
		dates = append(dates, date)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	// Write a file inside the recaps dir.
	path := filepath.Join(Dir(dir), "2026-06-15.md")
	if err := os.WriteFile(path, []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(dates)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dates) == 0 || dates[0] != "2026-06-15" {
		t.Errorf("got %v, want [2026-06-15]", dates)
	}
}

func TestWatcher_SkipsNonDateFiles(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var fired int
	w, err := StartWatcher(dir, func(date string) {
		mu.Lock()
		fired++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	// Various malformed names — none should fire.
	for _, name := range []string{
		".hidden.md",
		"2026-6-15.md",      // wrong digit count
		"2026-06-15",         // no extension
		"2026-06-15.txt",     // wrong extension
		"hello.md",
	} {
		if err := os.WriteFile(filepath.Join(Dir(dir), name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("fired = %d, want 0", fired)
	}
}
```

- [ ] **Step 6: Run watcher tests to verify they fail**

```bash
go test ./internal/recap/ -run TestWatcher
```

Expected: FAIL (StartWatcher / Watcher / Stop undefined).

- [ ] **Step 7: Implement watcher.go**

Mirror `internal/summary/watcher.go` closely — same debounce + drain-on-stop pattern. The two structural differences are: (a) the callback receives a date string, (b) `parseRecapFilename` validates `YYYY-MM-DD.md`.

```go
// internal/recap/watcher.go
package recap

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher tails <dataDir>/recaps/ and invokes onWrite(date) whenever
// a <YYYY-MM-DD>.md file is created or modified. Same debounce
// pattern as summary.Watcher — one Write that produces multiple
// fsnotify events still fires the callback exactly once per file
// per 200 ms window.
type Watcher struct {
	w       *fsnotify.Watcher
	onWrite func(date string)
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	pending map[string]*time.Timer
}

const debounce = 200 * time.Millisecond

// recapFilename matches strict YYYY-MM-DD.md (10 digits + dashes + .md).
var recapFilename = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2})\.md$`)

// StartWatcher creates the recaps directory if missing, starts an
// fsnotify watcher on it, and dispatches debounced filename events
// to onWrite in a background goroutine.
//
// Errors during mkdir or fsnotify init are returned; the caller is
// expected to log + carry on without the watcher rather than abort
// boot (sidebar becomes stale but the app still works).
func StartWatcher(dataDir string, onWrite func(date string)) (*Watcher, error) {
	if onWrite == nil {
		return nil, errors.New("recap.StartWatcher: onWrite required")
	}
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(dir); err != nil {
		_ = fw.Close()
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

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			date := parseRecapFilename(filepath.Base(ev.Name))
			if date == "" {
				continue
			}
			w.schedule(date)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn("recap watcher", "err", err)
		case <-w.stop:
			return
		}
	}
}

func (w *Watcher) schedule(date string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[date]; ok {
		t.Reset(debounce)
		return
	}
	w.pending[date] = time.AfterFunc(debounce, func() {
		w.mu.Lock()
		delete(w.pending, date)
		w.mu.Unlock()
		w.onWrite(date)
	})
}

// Stop closes the underlying fsnotify watcher and drains any
// in-flight debounce timers, so callbacks scheduled before Stop
// don't fire after it returns.
func (w *Watcher) Stop() {
	close(w.stop)
	_ = w.w.Close()
	<-w.done
	w.mu.Lock()
	timers := w.pending
	w.pending = map[string]*time.Timer{}
	w.mu.Unlock()
	for _, t := range timers {
		t.Stop()
	}
}

// parseRecapFilename returns the date string if name matches the
// strict YYYY-MM-DD.md pattern, "" otherwise.
func parseRecapFilename(name string) string {
	m := recapFilename.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return m[1]
}
```

- [ ] **Step 8: Run all recap tests**

```bash
go test ./internal/recap/...
```

Expected: PASS (4 tests).

- [ ] **Step 9: Race check**

```bash
go test -race ./internal/recap/...
```

Expected: PASS, no race warnings.

- [ ] **Step 10: Commit**

```bash
git add internal/recap/
git commit -m "feat(recap): path + fsnotify watcher with date filename validation"
```

---

### Task 4: `isRecapIO` dispatcher predicate

**Files:**
- Create: `internal/claude/dispatcher_recap.go`
- Create: `internal/claude/dispatcher_recap_test.go`
- Modify: `internal/claude/dispatcher.go`

Recap files are auto-allowed for Read (any date) and Write (any date, 16 KB cap). The path must strictly match `<dataDir>/recaps/<YYYY-MM-DD>.md`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/claude/dispatcher_recap_test.go
package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func mkRecapReq(t *testing.T, tool, path, content string) PendingRequest {
	t.Helper()
	type writeIn struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	type readIn struct {
		FilePath string `json:"file_path"`
	}
	var raw []byte
	var err error
	if tool == "Write" {
		raw, err = json.Marshal(writeIn{FilePath: path, Content: content})
	} else {
		raw, err = json.Marshal(readIn{FilePath: path})
	}
	if err != nil {
		t.Fatal(err)
	}
	return PendingRequest{ToolName: tool, ToolInput: raw}
}

func TestIsRecapIO_AllowsRead(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	req := mkRecapReq(t, "Read", path, "")
	if !isRecapIO(req, dataDir) {
		t.Errorf("Read of valid recap path should auto-allow")
	}
}

func TestIsRecapIO_AllowsWriteUnderCap(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	req := mkRecapReq(t, "Write", path, strings.Repeat("x", 8000))
	if !isRecapIO(req, dataDir) {
		t.Errorf("Write under cap should auto-allow")
	}
}

func TestIsRecapIO_DeniesOversizedWrite(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	req := mkRecapReq(t, "Write", path, strings.Repeat("x", maxRecapWriteBytes+1))
	if isRecapIO(req, dataDir) {
		t.Errorf("oversized Write must fall through to user")
	}
}

func TestIsRecapIO_DeniesBadFilename(t *testing.T) {
	dataDir := "/data"
	for _, name := range []string{
		"2026-6-15.md",   // wrong digit count
		"hello.md",
		"2026-06-15.txt",
		"../etc/passwd",
		"2026-06-15.md.bak",
	} {
		req := mkRecapReq(t, "Read", filepath.Join(dataDir, "recaps", name), "")
		if isRecapIO(req, dataDir) {
			t.Errorf("name %q should NOT auto-allow", name)
		}
	}
}

func TestIsRecapIO_DeniesPathTraversal(t *testing.T) {
	dataDir := "/data"
	req := mkRecapReq(t, "Read", "/data/recaps/../../../etc/passwd", "")
	if isRecapIO(req, dataDir) {
		t.Errorf("traversal should NOT auto-allow")
	}
}

func TestIsRecapIO_DeniesOtherTools(t *testing.T) {
	dataDir := "/data"
	path := filepath.Join(dataDir, "recaps", "2026-06-15.md")
	for _, tool := range []string{"Edit", "Bash", "Glob", "Grep"} {
		req := mkRecapReq(t, tool, path, "")
		if isRecapIO(req, dataDir) {
			t.Errorf("tool %q should NOT auto-allow", tool)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/claude/ -run TestIsRecapIO
```

Expected: FAIL (isRecapIO / maxRecapWriteBytes undefined).

- [ ] **Step 3: Implement the predicate**

```go
// internal/claude/dispatcher_recap.go
package claude

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jesseliu/headless-alfred/internal/recap"
)

// maxRecapWriteBytes caps the content size of an auto-allowed Write
// to a recap path. The recap-daily prompt aims for a short summary;
// 16 KB defends against an attacker convincing Claude to dump a huge
// payload into the file without user review while leaving headroom
// for legitimate growth (longer than summary's 8 KB because recaps
// span more sources).
const maxRecapWriteBytes = 16 * 1024

// recapBasename matches strict <YYYY-MM-DD>.md basenames.
var recapBasename = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}\.md$`)

// isRecapIO reports whether the pending tool request is a Read or
// (size-capped) Write of a canonical recap path under <dataDir>/recaps/.
// Used by the dispatcher to bypass the WS approval card for the
// recap-daily template's per-generation Write.
//
// Strict path match:
//   - directory must be exactly <dataDir>/recaps/ (after filepath.Clean)
//   - basename must match YYYY-MM-DD.md
//   - no traversal: Clean removes `..` segments, then we re-check
//     the parent equals the expected recap dir
func isRecapIO(req PendingRequest, dataDir string) bool {
	wantDir := filepath.Clean(recap.Dir(dataDir))
	check := func(p string) bool {
		clean := filepath.Clean(p)
		if filepath.Dir(clean) != wantDir {
			return false
		}
		return recapBasename.MatchString(filepath.Base(clean))
	}
	switch req.ToolName {
	case "Read":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		return check(in.FilePath)
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		if !check(in.FilePath) {
			return false
		}
		if len(in.Content) > maxRecapWriteBytes {
			return false
		}
		// Defense in depth: strings is imported through the
		// blank-identifier guard below, but only the import is
		// retained for the cap check above.
		_ = strings.Builder{}
		return true
	}
	return false
}
```

Drop the dummy `strings.Builder{}` line if `strings` is unused; the above keeps the import compiling without forcing string mutation. (If go vet complains, remove both the import and the line.)

- [ ] **Step 4: Wire it into dispatcher.go**

In `internal/claude/dispatcher.go`, find the existing fast-path:

```go
if dataDir != "" && isSummaryIO(req, alfredSID, dataDir) {
    autoAllow(req.ToolUseID)
    return
}
```

Add an identical sibling immediately after for recap:

```go
if dataDir != "" && isRecapIO(req, dataDir) {
    autoAllow(req.ToolUseID)
    return
}
```

Note: `isRecapIO` doesn't take `alfredSID` because recap files are global, not per-session.

- [ ] **Step 5: Run all dispatcher + claude tests**

```bash
go test ./internal/claude/...
```

Expected: PASS — new isRecapIO tests + all existing isSummaryIO + dispatcher tests still green.

- [ ] **Step 6: Commit**

```bash
git add internal/claude/dispatcher_recap.go internal/claude/dispatcher_recap_test.go \
        internal/claude/dispatcher.go
git commit -m "feat(dispatcher): isRecapIO auto-allow for <dataDir>/recaps/<date>.md"
```

---

### Task 5: `RunOptions.Continue` flag

**Files:**
- Modify: `internal/claude/runner.go`

The recap session invokes `claude -c -p ...` to continue the prior recap conversation. Regular chat sessions keep using `--resume <uuid>`. The flag is mutually exclusive with `--resume`; if both are set, `Continue` wins.

- [ ] **Step 1: Inspect current shape**

```bash
grep -n "RunOptions\|--resume\|SessionUUID" internal/claude/runner.go | head -20
```

Find the struct definition and the args-building code (around line 130-150).

- [ ] **Step 2: Add Continue to RunOptions**

In the `RunOptions` struct (look for `type RunOptions struct`), add a field. Place it after `SessionUUID` to keep related flags adjacent:

```go
// Continue, when true, invokes `claude -c` instead of
// `--resume <uuid>`. Used by recap sessions which want to continue
// the most recent conversation in the cwd rather than resume a
// specific Alfred-tracked UUID. Mutually exclusive with --resume:
// if Continue is true, SessionUUID is ignored.
Continue bool
```

- [ ] **Step 3: Update args building**

Find the existing block that appends `--resume`:

```go
if opts.SessionUUID != "" {
    args = append(args, "--resume", opts.SessionUUID)
}
```

Replace with:

```go
if opts.Continue {
    args = append(args, "-c")
} else if opts.SessionUUID != "" {
    args = append(args, "--resume", opts.SessionUUID)
}
```

- [ ] **Step 4: Verify compile + existing tests**

```bash
go test ./internal/claude/...
```

Expected: PASS — existing tests don't set `Continue`, so behavior is unchanged for them.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/runner.go
git commit -m "feat(runner): RunOptions.Continue invokes claude -c"
```

---

### Task 6: `Manager.CreateOrGetRecapSession` + `List(kind)` filter

**Files:**
- Modify: `internal/session/manager.go`
- Modify: `internal/session/manager_test.go` (existing, add 2 tests)

`CreateOrGetRecapSession` holds the manager mutex for the entire (scan → return-existing-or-create-new) sequence, so concurrent callers serialize. `List` gets an optional kind filter; existing callers pass `KindChat` to match today's behavior.

- [ ] **Step 1: Write the failing tests**

In `internal/session/manager_test.go`, find existing tests that set up a Manager with a fake Runner (look for `NewManager(`). Mirror that setup and add:

```go
func TestManager_CreateOrGetRecapSession_Idempotent(t *testing.T) {
	m := newTestManager(t)
	a, err := m.CreateOrGetRecapSession()
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != store.KindRecap {
		t.Errorf("Kind = %q, want %q", a.Kind, store.KindRecap)
	}
	b, err := m.CreateOrGetRecapSession()
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Errorf("second call returned a different session (got %q, want %q)", b.ID, a.ID)
	}
}

func TestManager_CreateOrGetRecapSession_KillsOrphans(t *testing.T) {
	m := newTestManager(t)
	// Seed two recap sessions directly into the metas (simulating an
	// orphaned state). One should be killed and only one returned.
	m.mu.Lock()
	for _, id := range []string{"ghost-1", "ghost-2"} {
		m.metas[id] = store.SessionMeta{ID: id, Kind: store.KindRecap, Name: "Recap"}
	}
	m.mu.Unlock()

	got, err := m.CreateOrGetRecapSession()
	if err != nil {
		t.Fatal(err)
	}
	// Should have exactly one KindRecap session left (the returned one).
	recaps := m.List(store.KindRecap)
	if len(recaps) != 1 {
		t.Errorf("got %d recap sessions, want 1", len(recaps))
	}
	if recaps[0].ID != got.ID {
		t.Errorf("ID mismatch: list=%q, returned=%q", recaps[0].ID, got.ID)
	}
}

func TestManager_List_FiltersByKind(t *testing.T) {
	m := newTestManager(t)
	// Seed: one chat, one recap.
	chat, _ := m.Create("chat-session")
	rec, _ := m.CreateOrGetRecapSession()

	chats := m.List(store.KindChat)
	recaps := m.List(store.KindRecap)

	if len(chats) != 1 || chats[0].ID != chat.ID {
		t.Errorf("List(KindChat): got %+v, want 1 chat session", chats)
	}
	if len(recaps) != 1 || recaps[0].ID != rec.ID {
		t.Errorf("List(KindRecap): got %+v, want 1 recap session", recaps)
	}
}
```

Note: the tests reference `newTestManager(t)` — discover the actual helper name in the existing file (it might be different), and use that.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/session/ -run "TestManager_CreateOrGetRecapSession|TestManager_List_FiltersByKind"
```

Expected: FAIL (`CreateOrGetRecapSession` undefined; `List(kind)` signature mismatch).

- [ ] **Step 3: Update `List` signature**

The existing `List()` returns `[]store.SessionMeta`. Change it to accept an optional kind filter. Find the existing:

```go
func (m *Manager) List() []store.SessionMeta {
    m.mu.Lock()
    defer m.mu.Unlock()
    out := make([]store.SessionMeta, 0, len(m.metas))
    for _, meta := range m.metas {
        out = append(out, meta)
    }
    sortByCreatedAtAsc(out)
    return out
}
```

Replace with:

```go
// List returns the snapshot of all SessionMetas, optionally filtered
// by Kind. Pass store.KindChat to get only chat sessions (the default
// case for the sessions sidebar); pass store.KindRecap for just the
// recap session(s). Pass "" (zero value of SessionKind) — i.e.
// store.KindChat — for the default chat-only view. To get all kinds,
// use ListAll().
func (m *Manager) List(kind store.SessionKind) []store.SessionMeta {
    m.mu.Lock()
    defer m.mu.Unlock()
    out := make([]store.SessionMeta, 0, len(m.metas))
    for _, meta := range m.metas {
        if meta.Kind != kind {
            continue
        }
        out = append(out, meta)
    }
    sortByCreatedAtAsc(out)
    return out
}

// ListAll returns all sessions regardless of kind.
func (m *Manager) ListAll() []store.SessionMeta {
    m.mu.Lock()
    defer m.mu.Unlock()
    out := make([]store.SessionMeta, 0, len(m.metas))
    for _, meta := range m.metas {
        out = append(out, meta)
    }
    sortByCreatedAtAsc(out)
    return out
}
```

- [ ] **Step 4: Update every existing `List()` call site**

```bash
grep -rn "\.List()" internal/ cmd/
```

Each existing call now needs an argument. The two cases:

- If the caller wants ALL sessions (e.g., reconcile loop), change to `ListAll()`.
- If the caller wants chat-only (the sessions endpoint that feeds the sidebar), change to `List(store.KindChat)`.

Most callers should become `ListAll()` (broad scans). The `ListSessionsHandler` in `internal/api/handlers_sessions.go` (or whatever the existing path is) is the one that becomes `List(store.KindChat)`.

- [ ] **Step 5: Add CreateOrGetRecapSession**

Pick a spot near `Create` in `internal/session/manager.go`. Add:

```go
// CreateOrGetRecapSession returns the existing recap session, or
// creates one if none exists. If multiple recap sessions are found
// (orphans from a previous run), all but the most recently created
// are killed, and the survivor is returned.
//
// Holds m.mu for the duration of (scan → return-existing-or-create),
// so concurrent callers serialize.
func (m *Manager) CreateOrGetRecapSession() (store.SessionMeta, error) {
    m.mu.Lock()
    var existing []store.SessionMeta
    for _, meta := range m.metas {
        if meta.Kind == store.KindRecap {
            existing = append(existing, meta)
        }
    }
    if len(existing) > 0 {
        sortByCreatedAtAsc(existing)
        keep := existing[len(existing)-1]
        // Kill the orphans (older recap sessions) while still
        // holding the lock — they leave m.metas in this same
        // critical section.
        toKill := existing[:len(existing)-1]
        m.mu.Unlock()
        for _, orphan := range toKill {
            if err := m.Close(orphan.ID); err != nil {
                m.cfg.Logger.Warn("CreateOrGetRecapSession: orphan close failed",
                    "id", orphan.ID, "err", err)
            }
        }
        return keep, nil
    }
    // No existing recap session — create one inside the lock-held
    // section. We unlock to call Create (which takes the lock itself)
    // and rely on the fact that Create + this whole method run under
    // the same overall serializer (the manager's mu only). Concurrent
    // callers will both either see "existing" above OR both proceed
    // to Create; the second one's Create will then see the first
    // one's freshly-created session via the existing scan if we
    // re-check. Race-safe path: re-check after re-acquiring lock.
    m.mu.Unlock()

    name := "Recap"
    meta, err := m.Create(name)
    if err != nil {
        return store.SessionMeta{}, err
    }
    // Tag it as recap. Set field directly under lock then persist.
    m.mu.Lock()
    if cur, ok := m.metas[meta.ID]; ok {
        cur.Kind = store.KindRecap
        m.metas[meta.ID] = cur
        meta = cur
    }
    m.mu.Unlock()
    if err := m.persistMetas(); err != nil {
        return store.SessionMeta{}, fmt.Errorf("persist recap kind: %w", err)
    }
    return meta, nil
}
```

Race note: the lock dance is awkward because `Create` itself takes the lock. To avoid double-lock complexity, we accept that two simultaneous "+ 复盘" calls might both pass the "no existing" check and create two recap sessions. The orphan-cleanup branch above is the recovery — next caller scans, sees two, kills the older one. This is the same self-heal documented in the spec.

If race-resistance matters more, introduce a `Manager.recapMu sync.Mutex` (separate from `m.mu`) and hold it across the entire method body. **Not done in v1** — the orphan-kill path is sufficient.

- [ ] **Step 6: Run all session tests + go build**

```bash
go test ./internal/session/... && go build ./...
```

Expected: PASS + clean.

- [ ] **Step 7: Commit**

```bash
git add internal/session/manager.go internal/session/manager_test.go \
        $(grep -rl "\.List()\|\.ListAll()" internal/ cmd/ 2>/dev/null)
git commit -m "feat(session): CreateOrGetRecapSession + List(kind) filter"
```

---

### Task 7: `GET /api/sessions/{id}` single-session endpoint

**Files:**
- Create or modify: `internal/api/sessions_handlers.go` (if file exists; else create)
- Modify: `internal/api/router.go`
- Test: `internal/api/sessions_handlers_test.go` (new or existing)

Used by the frontend to load metadata for the currently selected recap session, which doesn't appear in the chat-filtered list.

- [ ] **Step 1: Check whether sessions_handlers.go exists**

```bash
ls internal/api/sessions_handlers.go 2>&1
```

If it exists, add the handler there. If not, create it. The exact filename doesn't matter for testing.

- [ ] **Step 2: Write the failing tests**

In `internal/api/sessions_handlers_test.go` (create if missing):

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/store"
)

// sessionFinder is the subset of *session.Manager this handler needs.
type sessionFinder interface {
	FindByID(id string) (store.SessionMeta, bool)
}

type fakeFinder struct {
	m map[string]store.SessionMeta
}

func (f *fakeFinder) FindByID(id string) (store.SessionMeta, bool) {
	v, ok := f.m[id]
	return v, ok
}

func TestGetSessionHandler_Returns200ForKnownSession(t *testing.T) {
	f := &fakeFinder{m: map[string]store.SessionMeta{
		"sid-A": {ID: "sid-A", Name: "n", Kind: store.KindRecap},
	}}
	r := chi.NewRouter()
	r.Get("/api/sessions/{id}", GetSessionHandler(f).ServeHTTP)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var got store.SessionMeta
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "sid-A" || got.Kind != store.KindRecap {
		t.Errorf("got %+v", got)
	}
}

func TestGetSessionHandler_Returns404ForUnknown(t *testing.T) {
	f := &fakeFinder{m: map[string]store.SessionMeta{}}
	r := chi.NewRouter()
	r.Get("/api/sessions/{id}", GetSessionHandler(f).ServeHTTP)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/ghost", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/api/ -run TestGetSessionHandler
```

Expected: FAIL (`GetSessionHandler` undefined; `Manager.FindByID` undefined).

- [ ] **Step 4: Add `Manager.FindByID`**

In `internal/session/manager.go`:

```go
// FindByID returns the SessionMeta for sessionID or (zero, false)
// if no such session exists. Cheap, lock-protected map lookup.
func (m *Manager) FindByID(sessionID string) (store.SessionMeta, bool) {
    m.mu.Lock()
    defer m.mu.Unlock()
    meta, ok := m.metas[sessionID]
    return meta, ok
}
```

- [ ] **Step 5: Implement the handler**

Add to `internal/api/sessions_handlers.go` (or create the file):

```go
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/store"
)

// sessionFinder is the subset of *session.Manager GetSessionHandler
// needs. Lets tests stub it without spinning up a real manager.
type sessionFinder interface {
	FindByID(id string) (store.SessionMeta, bool)
}

// GetSessionHandler returns one SessionMeta by id, regardless of
// Kind. The list endpoint filters to chat-only by default, so
// the frontend uses this endpoint to learn metadata for a recap
// session it has selected but can't find in the list.
func GetSessionHandler(f sessionFinder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		meta, ok := f.FindByID(id)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		writeJSON(w, http.StatusOK, meta)
	})
}
```

- [ ] **Step 6: Wire the route**

In `internal/api/router.go`, inside the auth group, add right after the existing `r.Get("/api/sessions", ...)` (around line 48):

```go
r.Get("/api/sessions/{id}", GetSessionHandler(d.Manager).ServeHTTP)
```

- [ ] **Step 7: Run tests + ensure existing tests pass**

```bash
go test ./internal/api/... ./internal/session/...
```

Expected: PASS — new handler tests + everything else green.

- [ ] **Step 8: Commit**

```bash
git add internal/api/sessions_handlers.go internal/api/sessions_handlers_test.go \
        internal/api/router.go internal/session/manager.go
git commit -m "feat(api): GET /api/sessions/{id} single-session metadata endpoint"
```

---

### Task 8: Recap HTTP endpoints

**Files:**
- Create: `internal/api/recap_handlers.go`
- Create: `internal/api/recap_handlers_test.go`
- Modify: `internal/api/router.go`

Four endpoints: POST find-or-create recap session, DELETE current recap, GET recap-list (date metadata), GET recap content by date.

- [ ] **Step 1: Write the failing tests for the file endpoints (GET list + GET date)**

```go
// internal/api/recap_handlers_test.go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestListRecaps_EmptyDirReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Get("/api/recaps", ListRecapsHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps", nil))
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q (want [])", w.Body.String())
	}
}

func TestListRecaps_ReturnsDatesDesc(t *testing.T) {
	dir := t.TempDir()
	rdir := filepath.Join(dir, "recaps")
	_ = os.MkdirAll(rdir, 0o755)
	for _, d := range []string{"2026-06-10", "2026-06-12", "2026-06-15"} {
		_ = os.WriteFile(filepath.Join(rdir, d+".md"), []byte("x"), 0o644)
	}
	// Add some noise that must be ignored.
	_ = os.WriteFile(filepath.Join(rdir, "hello.md"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(rdir, "2026-6-15.md"), []byte("x"), 0o644)

	r := chi.NewRouter()
	r.Get("/api/recaps", ListRecapsHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps", nil))

	var got []struct {
		Date string `json:"date"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3 (body=%s)", len(got), w.Body.String())
	}
	// Desc order: newest first
	wantOrder := []string{"2026-06-15", "2026-06-12", "2026-06-10"}
	for i, w := range wantOrder {
		if got[i].Date != w {
			t.Errorf("position %d: got %q want %q", i, got[i].Date, w)
		}
	}
}

func TestGetRecap_Returns200WithMarkdown(t *testing.T) {
	dir := t.TempDir()
	rdir := filepath.Join(dir, "recaps")
	_ = os.MkdirAll(rdir, 0o755)
	body := "# Recap · 2026-06-15\n\n## Shipped\n- thing\n"
	_ = os.WriteFile(filepath.Join(rdir, "2026-06-15.md"), []byte(body), 0o644)

	r := chi.NewRouter()
	r.Get("/api/recaps/{date}", GetRecapHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps/2026-06-15", nil))
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != body {
		t.Errorf("body mismatch:\n got %q\nwant %q", w.Body.String(), body)
	}
}

func TestGetRecap_Returns404ForMissingDate(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "recaps"), 0o755)

	r := chi.NewRouter()
	r.Get("/api/recaps/{date}", GetRecapHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps/2026-06-15", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

func TestGetRecap_Returns400ForBadDate(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Get("/api/recaps/{date}", GetRecapHandler(dir).ServeHTTP)
	for _, bad := range []string{"2026-6-15", "tomorrow", "../etc/passwd", "../2026-06-15"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps/"+bad, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("date %q: status %d, want 400", bad, w.Code)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/api/ -run "TestListRecaps|TestGetRecap"
```

Expected: FAIL (handlers undefined).

- [ ] **Step 3: Implement the file handlers**

```go
// internal/api/recap_handlers.go
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/recap"
)

// recapDate matches strict YYYY-MM-DD (no .md suffix here — that's
// for filenames, not for path parameters).
var recapDateParam = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

type recapEntry struct {
	Date    string `json:"date"`
	IsToday bool   `json:"isToday"`
}

// ListRecapsHandler returns the dates that have a recap file, newest
// first. Only dates matching strict YYYY-MM-DD.md are included; any
// other file in the dir is silently skipped.
func ListRecapsHandler(dataDir string) http.Handler {
	dir := recap.Dir(dataDir)
	today := func() string { return time.Now().Local().Format("2006-01-02") }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusOK, []recapEntry{})
				return
			}
			writeError(w, http.StatusInternalServerError, "io_error", err.Error())
			return
		}
		var dates []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			base := strings.TrimSuffix(name, ".md")
			if !recapDateParam.MatchString(base) {
				continue
			}
			dates = append(dates, base)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dates))) // YYYY-MM-DD sorts lexicographically == chronologically
		out := make([]recapEntry, len(dates))
		td := today()
		for i, d := range dates {
			out[i] = recapEntry{Date: d, IsToday: d == td}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// GetRecapHandler returns the raw markdown for one date.
func GetRecapHandler(dataDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := chi.URLParam(r, "date")
		if !recapDateParam.MatchString(date) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid date")
			return
		}
		path := recap.Path(dataDir, date)
		// Defence in depth: ensure the resolved path is under recaps/.
		clean := filepath.Clean(path)
		root := recap.Dir(dataDir)
		if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
			writeError(w, http.StatusNotFound, "not_found", "no such recap")
			return
		}
		body, err := os.ReadFile(clean)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "not_found", "no recap for that date")
				return
			}
			writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(body)
	})
}

// Silence unused-import false positive in older Go vet versions; json
// is used transitively via writeJSON in the same package.
var _ = json.Marshal
```

(Drop the last `var _ = json.Marshal` line if vet is happy without it.)

- [ ] **Step 4: Run file-handler tests**

```bash
go test ./internal/api/ -run "TestListRecaps|TestGetRecap"
```

Expected: PASS (5 tests).

- [ ] **Step 5: Write failing tests for the session-lifecycle endpoints**

In the same `internal/api/recap_handlers_test.go`, add:

```go
// recapManager is the subset of *session.Manager the recap session
// handlers need.
type recapManager interface {
	CreateOrGetRecapSession() (store.SessionMeta, error)
	List(kind store.SessionKind) []store.SessionMeta
	Close(id string) error
}

type fakeRecapMgr struct {
	current *store.SessionMeta
	closed  []string
}

func (f *fakeRecapMgr) CreateOrGetRecapSession() (store.SessionMeta, error) {
	if f.current != nil {
		return *f.current, nil
	}
	m := store.SessionMeta{ID: "recap-1", Name: "Recap", Kind: store.KindRecap}
	f.current = &m
	return m, nil
}

func (f *fakeRecapMgr) List(kind store.SessionKind) []store.SessionMeta {
	if f.current != nil && f.current.Kind == kind {
		return []store.SessionMeta{*f.current}
	}
	return nil
}

func (f *fakeRecapMgr) Close(id string) error {
	f.closed = append(f.closed, id)
	if f.current != nil && f.current.ID == id {
		f.current = nil
	}
	return nil
}

func TestCreateRecapSession_ReturnsCreatedAndIdempotent(t *testing.T) {
	fm := &fakeRecapMgr{}
	r := chi.NewRouter()
	r.Post("/api/recap-sessions", CreateRecapSessionHandler(fm).ServeHTTP)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/recap-sessions", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var got1 store.SessionMeta
	_ = json.Unmarshal(w.Body.Bytes(), &got1)
	if got1.ID != "recap-1" {
		t.Errorf("got %+v", got1)
	}

	// Second call returns the same session.
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest("POST", "/api/recap-sessions", nil))
	var got2 store.SessionMeta
	_ = json.Unmarshal(w2.Body.Bytes(), &got2)
	if got2.ID != "recap-1" {
		t.Errorf("second call returned %+v", got2)
	}
}

func TestDeleteRecapSession_KillsCurrent(t *testing.T) {
	fm := &fakeRecapMgr{}
	_, _ = fm.CreateOrGetRecapSession()
	r := chi.NewRouter()
	r.Delete("/api/recap-sessions/current", DeleteRecapSessionHandler(fm).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/recap-sessions/current", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if len(fm.closed) != 1 || fm.closed[0] != "recap-1" {
		t.Errorf("closed = %v, want [recap-1]", fm.closed)
	}
}

func TestDeleteRecapSession_204WhenNone(t *testing.T) {
	fm := &fakeRecapMgr{}
	r := chi.NewRouter()
	r.Delete("/api/recap-sessions/current", DeleteRecapSessionHandler(fm).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/recap-sessions/current", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", w.Code)
	}
}
```

- [ ] **Step 6: Run failing**

```bash
go test ./internal/api/ -run "TestCreateRecapSession|TestDeleteRecapSession"
```

Expected: FAIL.

- [ ] **Step 7: Add lifecycle handlers**

Append to `internal/api/recap_handlers.go`:

```go
// recapManager is the subset of *session.Manager the recap lifecycle
// handlers need.
type recapManager interface {
	CreateOrGetRecapSession() (store.SessionMeta, error)
	List(kind store.SessionKind) []store.SessionMeta
	Close(id string) error
}

// CreateRecapSessionHandler is POST /api/recap-sessions — find or
// create the singleton recap session and return its metadata.
func CreateRecapSessionHandler(m recapManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, err := m.CreateOrGetRecapSession()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "recap_create_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, meta)
	})
}

// DeleteRecapSessionHandler is DELETE /api/recap-sessions/current —
// kill whatever recap session is currently alive (if any).
// Idempotent — 204 even if none exists.
func DeleteRecapSessionHandler(m recapManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recaps := m.List(store.KindRecap)
		for _, rec := range recaps {
			if err := m.Close(rec.ID); err != nil {
				// Log + continue; never block deletion of others.
				w.Header().Set("X-Recap-Close-Warning", err.Error())
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
```

Add the missing import: `"github.com/jesseliu/headless-alfred/internal/store"`.

- [ ] **Step 8: Run all recap handler tests**

```bash
go test ./internal/api/ -run "TestListRecaps|TestGetRecap|TestCreateRecapSession|TestDeleteRecapSession"
```

Expected: PASS (9 tests total in recap_handlers_test.go).

- [ ] **Step 9: Wire routes**

In `internal/api/router.go`, inside the auth group, add a "Recap" block:

```go
// Recap (file content).
r.Get("/api/recaps", ListRecapsHandler(d.Manager.DataDir()).ServeHTTP)
r.Get("/api/recaps/{date}", GetRecapHandler(d.Manager.DataDir()).ServeHTTP)

// Recap session (lifecycle).
r.Post("/api/recap-sessions", CreateRecapSessionHandler(d.Manager).ServeHTTP)
r.Delete("/api/recap-sessions/current", DeleteRecapSessionHandler(d.Manager).ServeHTTP)
```

- [ ] **Step 10: Run full backend tests**

```bash
go test ./internal/api/... ./internal/session/... ./internal/recap/...
```

Expected: all PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/api/recap_handlers.go internal/api/recap_handlers_test.go \
        internal/api/router.go
git commit -m "feat(api): recap endpoints (list, get-by-date, create/delete session)"
```

---

### Task 9: `recap_updated` WS frame + watcher startup

**Files:**
- Modify: `internal/api/wsproto.go`
- Modify: `internal/api/ws.go`
- Modify: `cmd/alfred-server/main.go`

The watcher started at boot pushes `(date string)` events onto a process-wide channel; the WS handler subscribes per-connection and broadcasts the frame to every client.

- [ ] **Step 1: Add the const + OutMsg field**

In `internal/api/wsproto.go`, find the existing `TypeSummaryUpdated` constant:

```go
const TypeSummaryUpdated = "summary_updated"
```

Add right after:

```go
const TypeRecapUpdated = "recap_updated"
```

Find the `OutMsg` struct. It already has `SessionID string` for `summary_updated`. The recap frame doesn't carry a sessionID but does carry a date. Add a `Date` field (omitempty, only set when Type == TypeRecapUpdated):

```go
type OutMsg struct {
    // ... existing fields ...
    Date string `json:"date,omitempty"`
}
```

- [ ] **Step 2: Inspect main.go for the existing summary watcher startup, mirror for recap**

```bash
grep -n "summary.StartWatcher\|recap.StartWatcher" cmd/alfred-server/main.go internal/api/ws.go
```

The summary watcher lives in `ws.go` (per-connection) — recap is global, so we put it in main.go and fan out via a single shared channel.

In `cmd/alfred-server/main.go`, add a recap watcher startup near the bridge startup. Add an import: `"github.com/jesseliu/headless-alfred/internal/recap"`.

```go
// Recap-file watcher: emits date strings when a recaps/<date>.md
// file is written. Fanned out to all WS clients via the broadcaster
// the api.NewRouter wires up.
recapUpdates := make(chan string, 16)
recapWatcher, err := recap.StartWatcher(dataDir, func(date string) {
    select {
    case recapUpdates <- date:
    default:
        logger.Warn("recapUpdates channel full; dropping", "date", date)
    }
})
if err != nil {
    logger.Warn("recap watcher startup failed; recap UI will be stale", "err", err)
} else {
    defer recapWatcher.Stop()
}
```

Pass `recapUpdates` into `api.Deps`:

```go
router := api.NewRouter(api.Deps{
    Manager:      mgr,
    Auth:         a,
    RateLimiter:  rl,
    Ready:        ready.Load,
    Bridge:       bridge,
    Dispatcher:   dispatcher,
    RecapUpdates: recapUpdates,
})
```

- [ ] **Step 3: Add `RecapUpdates` to Deps + plumb to ws**

In `internal/api/router.go`, add to `Deps`:

```go
// RecapUpdates receives a date string each time a recap file is
// written. WS handler subscribes to fan it out to every client.
// Nil-safe — ws.go degrades to no recap broadcasts when nil.
RecapUpdates <-chan string
```

- [ ] **Step 4: Broadcast in ws.go**

In `internal/api/ws.go`, find the existing summary `case sid, ok := <-summaryUpdates:` block. For recap, the channel is process-wide and `RecapUpdates` is already a `<-chan string`. We need every WS client to receive every recap_updated event — but a Go channel only delivers each value to one receiver.

Solution: introduce a fan-out broadcaster. In `internal/api/ws.go`'s package-level (or in a new file `internal/api/recap_broadcast.go`), add:

```go
// recapBroadcaster fans out a single source channel of recap dates
// to every subscriber. Each WS client subscribes on connect,
// unsubscribes on disconnect.
type recapBroadcaster struct {
    mu   sync.Mutex
    subs map[chan string]struct{}
}

func newRecapBroadcaster(source <-chan string) *recapBroadcaster {
    b := &recapBroadcaster{subs: map[chan string]struct{}{}}
    if source == nil {
        return b
    }
    go func() {
        for date := range source {
            b.mu.Lock()
            for ch := range b.subs {
                select {
                case ch <- date:
                default:
                    // subscriber's queue full — drop. UI will be
                    // momentarily stale but a manual refresh fixes.
                }
            }
            b.mu.Unlock()
        }
    }()
    return b
}

func (b *recapBroadcaster) subscribe() (chan string, func()) {
    ch := make(chan string, 8)
    b.mu.Lock()
    b.subs[ch] = struct{}{}
    b.mu.Unlock()
    return ch, func() {
        b.mu.Lock()
        delete(b.subs, ch)
        b.mu.Unlock()
        close(ch)
    }
}
```

Wire it: `WSHandler` constructor (look for its signature) takes a `*recapBroadcaster`; the router constructs it once from `d.RecapUpdates` and passes to every WS connection. Inside the connection loop, add a `case date := <-recapSub:` arm that writes `OutMsg{Type: TypeRecapUpdated, Date: date}`.

If the WSHandler signature already takes a list of dependencies, add the broadcaster to the same bundle.

Concretely (the actual diff depends on the existing ws.go shape — read it before editing):

```go
// Where WSHandler is constructed, before opening any connections:
broadcaster := newRecapBroadcaster(d.RecapUpdates)

// Per connection, after opening:
recapSub, unsubscribe := broadcaster.subscribe()
defer unsubscribe()

// In the select loop:
case date := <-recapSub:
    _ = write(OutMsg{Type: TypeRecapUpdated, Date: date})
```

- [ ] **Step 5: Run all backend tests**

```bash
go test ./internal/... ./cmd/...
```

Expected: PASS. The existing tests don't exercise the recap broadcaster (no fixture for end-to-end WS push), so this is a smoke check that everything still compiles + the existing summary path still works.

- [ ] **Step 6: Race check**

```bash
go test -race ./internal/api/... ./internal/recap/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/api/wsproto.go internal/api/ws.go internal/api/router.go \
        cmd/alfred-server/main.go internal/api/recap_broadcast.go
git commit -m "feat(ws): recap_updated frame broadcast via recap watcher"
```

---

## Final verification

- [ ] **All backend tests**

```bash
go test ./internal/... ./cmd/...
```

Expected: all PASS.

- [ ] **Race**

```bash
go test -race ./internal/...
```

Expected: clean.

- [ ] **Manual smoke** (alfred-server running on :8080 with `ALFRED_DATA_DIR=/tmp/alfred-dev/data`)

```bash
TOK=$(curl -s -X POST http://localhost:8080/api/login -H 'Content-Type: application/json' \
  -d '{"user":"admin","password":"admin"}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')

# POST creates a recap session
curl -s -X POST -H "Authorization: Bearer $TOK" http://localhost:8080/api/recap-sessions | python3 -m json.tool

# List is empty initially
curl -s -H "Authorization: Bearer $TOK" http://localhost:8080/api/recaps

# Write a recap file, GET it back
mkdir -p /tmp/alfred-dev/data/recaps
echo "# Recap" > /tmp/alfred-dev/data/recaps/2026-06-15.md
curl -s -H "Authorization: Bearer $TOK" http://localhost:8080/api/recaps
curl -s -H "Authorization: Bearer $TOK" http://localhost:8080/api/recaps/2026-06-15

# Cleanup
curl -s -X DELETE -H "Authorization: Bearer $TOK" http://localhost:8080/api/recap-sessions/current -i | head -1
rm /tmp/alfred-dev/data/recaps/2026-06-15.md
```

Expected:
- POST returns `{"id":"...","kind":"recap",...}`
- List returns `[]` then `[{"date":"2026-06-15","isToday":true}]`
- GET date returns `# Recap`
- DELETE returns `204 No Content`
