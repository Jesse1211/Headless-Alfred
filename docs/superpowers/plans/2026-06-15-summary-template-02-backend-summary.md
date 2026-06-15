# Summary template — Plan 02: Backend summary persistence + dispatcher auto-allow

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let Claude actually maintain the on-disk summary file without nagging the user for permission every turn, and expose its contents via HTTP so the frontend can render the right-hand sidebar.

**Architecture:** `internal/summary/path.go` (created as a stub in plan 01) gets real tests + the constant for the auto-allow guard. `internal/claude/dispatcher.go` gains an `isSummaryIO` fast-path before the normal channel-push that bypasses the WS approval card for `Read` and (size-capped) `Write` on the exact summary path. A new `GET /api/sessions/{sid}/summary` endpoint serves the file body (404 on miss).

**Tech Stack:** Go 1.25, `chi` for routing, `go test`.

After this plan: Claude can Read+Write the per-session summary without user-visible cards, and the frontend can `fetch()` the file body via REST. Watcher + push are plan 03; frontend wiring is plan 04.

**Depends on plan 01:** `internal/summary/path.go` already exists (just `Path(dataDir, sid)`).

---

### Task 1: `Path` test + a small `Dir` helper

**Files:**
- Modify: `internal/summary/path.go` (add `Dir(dataDir) string`)
- Test: `internal/summary/path_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/summary/path_test.go`:

```go
package summary

import (
	"path/filepath"
	"testing"
)

func TestPath_JoinsDataDirAndSidWithMdExt(t *testing.T) {
	got := Path("/data", "01KSESSION")
	want := filepath.Join("/data", "summaries", "01KSESSION.md")
	if got != want {
		t.Errorf("Path=%q, want %q", got, want)
	}
}

func TestDir_IsSummariesUnderDataDir(t *testing.T) {
	got := Dir("/data")
	want := filepath.Join("/data", "summaries")
	if got != want {
		t.Errorf("Dir=%q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/summary/...`
Expected: build failure ("undefined: Dir").

- [ ] **Step 3: Add Dir helper**

Modify `internal/summary/path.go`:

```go
// Package summary owns the per-session summary file: its on-disk
// path, the watcher that notices writes to it, and helpers shared
// by the prompt-injection path and the HTTP handler.
package summary

import "path/filepath"

// Path returns the on-disk summary path for the session.
// <dataDir>/summaries/<sessionID>.md
func Path(dataDir, sessionID string) string {
	return filepath.Join(Dir(dataDir), sessionID+".md")
}

// Dir returns the directory holding all summary files. Useful for
// fsnotify watchers and for tests that want to seed files.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "summaries")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/summary/...`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/summary/path.go internal/summary/path_test.go
git commit -m "feat(summary): Path + Dir helpers with tests"
```

---

### Task 2: `isSummaryIO` predicate — `internal/claude/dispatcher_summary.go`

**Files:**
- Create: `internal/claude/dispatcher_summary.go`
- Test: `internal/claude/dispatcher_summary_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claude/dispatcher_summary_test.go`:

```go
package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func mkReq(t *testing.T, tool, filePath, content string) PendingRequest {
	t.Helper()
	in := map[string]any{"file_path": filePath}
	if content != "" {
		in["content"] = content
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return PendingRequest{ToolName: tool, ToolInput: raw}
}

func TestIsSummaryIO_AllowsReadOfCanonicalPath(t *testing.T) {
	r := mkReq(t, "Read", "/data/summaries/01SID.md", "")
	if !isSummaryIO(r, "01SID", "/data") {
		t.Error("Read of canonical path should be auto-allow eligible")
	}
}

func TestIsSummaryIO_AllowsSmallWriteOfCanonicalPath(t *testing.T) {
	r := mkReq(t, "Write", "/data/summaries/01SID.md", "## Goal\nfoo")
	if !isSummaryIO(r, "01SID", "/data") {
		t.Error("small Write of canonical path should be auto-allow eligible")
	}
}

func TestIsSummaryIO_RejectsOversizedWrite(t *testing.T) {
	huge := strings.Repeat("x", 9*1024) // 9 KB > 8 KB cap
	r := mkReq(t, "Write", "/data/summaries/01SID.md", huge)
	if isSummaryIO(r, "01SID", "/data") {
		t.Error("oversized Write must fall through to the normal approval path")
	}
}

func TestIsSummaryIO_RejectsOtherPaths(t *testing.T) {
	for _, p := range []string{
		"/data/summaries/OTHER.md",          // wrong sid
		"/data/summaries/01SID.md.bak",      // suffix
		"/data/summaries/../sessions.json",  // path traversal
		"/etc/passwd",                       // wildly unrelated
		"/data/summaries/01SID.md/extra",    // subpath
	} {
		r := mkReq(t, "Write", p, "x")
		if isSummaryIO(r, "01SID", "/data") {
			t.Errorf("path %q must not be auto-allow eligible", p)
		}
	}
}

func TestIsSummaryIO_RejectsOtherTools(t *testing.T) {
	for _, tool := range []string{"Bash", "Edit", "Glob", "Grep"} {
		r := mkReq(t, tool, "/data/summaries/01SID.md", "x")
		if isSummaryIO(r, "01SID", "/data") {
			t.Errorf("tool %q must not be auto-allow eligible", tool)
		}
	}
}

func TestIsSummaryIO_RejectsMalformedInput(t *testing.T) {
	r := PendingRequest{ToolName: "Write", ToolInput: []byte(`not-json`)}
	if isSummaryIO(r, "01SID", "/data") {
		t.Error("malformed tool_input must not be auto-allow eligible")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/...`
Expected: build failure ("undefined: isSummaryIO").

- [ ] **Step 3: Implement the predicate**

Create `internal/claude/dispatcher_summary.go`:

```go
package claude

import (
	"encoding/json"

	"github.com/jesseliu/headless-alfred/internal/summary"
)

// maxSummaryWriteBytes caps the content size of an auto-allowed
// Write to the summary path. The template asks Claude to keep the
// file under 1500 characters; 8 KB gives ample headroom and still
// defends against an attacker convincing Claude to dump a huge
// payload into the file without user review.
const maxSummaryWriteBytes = 8 * 1024

// isSummaryIO reports whether the pending tool request is a
// Read or (size-capped) Write of the canonical summary path for
// the matched Alfred session. Used by the dispatcher to bypass
// the WS approval card for the summary template's per-turn churn.
//
// Strict string-equal match on file_path. Path traversal is not
// possible because alfredSID is the session ID we already
// resolved on the server side, never user input.
func isSummaryIO(req PendingRequest, alfredSID, dataDir string) bool {
	wantPath := summary.Path(dataDir, alfredSID)
	switch req.ToolName {
	case "Read":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		return in.FilePath == wantPath
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		if in.FilePath != wantPath {
			return false
		}
		if len(in.Content) > maxSummaryWriteBytes {
			return false
		}
		return true
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/claude/...`
Expected: PASS (existing tests still green + 6 new tests).

- [ ] **Step 5: Commit**

```bash
git add internal/claude/dispatcher_summary.go internal/claude/dispatcher_summary_test.go
git commit -m "feat(claude): isSummaryIO predicate guards dispatcher auto-allow"
```

---

### Task 3: Wire `isSummaryIO` into `OnAsk`

**Files:**
- Modify: `internal/claude/dispatcher.go` (extend `OnAsk` signature with a `dataDir` arg)
- Modify: `internal/claude/dispatcher_test.go` (update existing call sites)
- Modify: `cmd/alfred-server/main.go` (pass `mgr.DataDir()` into `OnAsk`)

- [ ] **Step 1: Write the failing test**

Add this to `internal/claude/dispatcher_test.go` (append at end):

```go
func TestDispatcher_SummaryWrite_AutoAllows(t *testing.T) {
	d := NewDispatcher()
	ch, unsub := d.SubscribeAsks("alfred-A")
	defer unsub()

	var allowed int
	autoAllow := func(string) { allowed++ }
	autoDeny := func(string, string) {}

	onAsk := d.OnAsk(
		func(s string) string { if s == "claude-1" { return "alfred-A" }; return "" },
		autoAllow,
		autoDeny,
		"/data", // new dataDir argument
	)

	// Build a small Write to the canonical path.
	req := PendingRequest{
		ToolUseID: "tu-summary",
		SessionID: "claude-1",
		ToolName:  "Write",
		ToolInput: []byte(`{"file_path":"/data/summaries/alfred-A.md","content":"## Goal\nfoo"}`),
	}
	onAsk(req)

	if allowed != 1 {
		t.Errorf("allowed=%d, want 1 (auto-allow summary write)", allowed)
	}
	select {
	case got := <-ch:
		t.Errorf("summary write must NOT push to subscriber channel; got %+v", got)
	default:
	}
}
```

- [ ] **Step 2: Update the existing test call sites for the new signature**

In `internal/claude/dispatcher_test.go`, every existing call to `d.OnAsk(lookup, autoAllow, autoDeny)` becomes `d.OnAsk(lookup, autoAllow, autoDeny, "")` — empty dataDir means the summary fast-path never triggers, preserving the old test semantics. There are 5 call sites today (RoutesToSubscriber, NoSubscriber_AutoDeny, UnknownClaudeConvo_AutoAllow, ResubscribeClosesPrior, FullBuffer_AutoDeny). Add the trailing `, ""` to each.

- [ ] **Step 3: Run tests to verify failures are the expected ones**

Run: `go test ./internal/claude/...`
Expected: build failure ("too many arguments to OnAsk" until we add the param) — that's fine, we're TDD-ing the signature change.

- [ ] **Step 4: Update `OnAsk` signature + wire the fast-path**

Modify `internal/claude/dispatcher.go`. Change the `OnAsk` signature and body:

```go
// OnAsk returns an onAsk callback suitable for passing to NewBridge.
// (existing comment kept; extend with the dataDir parameter docs)
//
// dataDir is the server's data root (Manager.DataDir()). It feeds
// the summary fast-path: tool calls that match isSummaryIO for the
// matched Alfred session skip the WS subscriber entirely and are
// auto-allowed. This avoids popping an approval card every turn for
// the summary-todo template's Read+Write churn.
func (d *Dispatcher) OnAsk(
	lookup func(claudeConvoID string) string,
	autoAllow func(toolUseID string),
	autoDeny func(toolUseID string, reason string),
	dataDir string,
) func(PendingRequest) {
	return func(req PendingRequest) {
		alfredSID := lookup(req.SessionID)
		if alfredSID == "" {
			slog.Debug("dispatcher: not an Alfred session, auto-allow",
				"claudeConvoID", req.SessionID, "tool", req.ToolName)
			autoAllow(req.ToolUseID)
			return
		}
		// Summary template's per-turn Read+Write of the canonical
		// summary file. Strict path + tool + size check inside;
		// anything off-pattern still goes through the WS card.
		if dataDir != "" && isSummaryIO(req, alfredSID, dataDir) {
			autoAllow(req.ToolUseID)
			return
		}
		d.mu.Lock()
		ch, ok := d.subs[alfredSID]
		d.mu.Unlock()
		if !ok {
			slog.Warn("dispatcher: no UI client subscribed, auto-deny",
				"session", alfredSID, "tool", req.ToolName, "toolUseID", req.ToolUseID)
			autoDeny(req.ToolUseID, "no UI client subscribed for session "+alfredSID)
			return
		}
		select {
		case ch <- req:
			slog.Debug("dispatcher: routed approval to UI",
				"session", alfredSID, "tool", req.ToolName, "toolUseID", req.ToolUseID)
		default:
			slog.Warn("dispatcher: queue full, auto-deny",
				"session", alfredSID, "tool", req.ToolName, "toolUseID", req.ToolUseID)
			autoDeny(req.ToolUseID, "approval queue full for this session")
		}
	}
}
```

- [ ] **Step 5: Update `cmd/alfred-server/main.go` caller**

In `cmd/alfred-server/main.go`, find the existing call to `dispatcher.OnAsk(mgr.FindByClaudeConvoID, ...)`. It currently passes 3 args:

```go
	bridge = claude.NewBridge(dispatcher.OnAsk(
		mgr.FindByClaudeConvoID,
		func(toolUseID string) {
			bridge.Resolve(toolUseID, claude.Decision{Permission: "allow"})
		},
		func(toolUseID, reason string) {
			bridge.Resolve(toolUseID, claude.Decision{Permission: "deny", Reason: reason})
		},
	))
```

Add `mgr.DataDir()` as the fourth arg:

```go
	bridge = claude.NewBridge(dispatcher.OnAsk(
		mgr.FindByClaudeConvoID,
		func(toolUseID string) {
			bridge.Resolve(toolUseID, claude.Decision{Permission: "allow"})
		},
		func(toolUseID, reason string) {
			bridge.Resolve(toolUseID, claude.Decision{Permission: "deny", Reason: reason})
		},
		mgr.DataDir(),
	))
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/...`
Expected: PASS.

- [ ] **Step 7: Build the server**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/claude/dispatcher.go internal/claude/dispatcher_test.go cmd/alfred-server/main.go
git commit -m "feat(dispatcher): auto-allow summary Read+Write via isSummaryIO fast-path"
```

---

### Task 4: `GET /api/sessions/{sid}/summary` endpoint

**Files:**
- Create: `internal/api/summary_handler.go`
- Modify: `internal/api/router.go` (add route, pass Deps)
- Test: `internal/api/summary_handler_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/api/summary_handler_test.go`:

```go
package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/summary"
)

// We test the handler against a hand-built chi router so the
// {sid} URL param resolves the same way it will in production.
func mountSummaryRouter(t *testing.T, dataDir string) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/summary", GetSummaryHandler(dataDir).ServeHTTP)
	return r
}

func TestGetSummary_FileExists_Returns200(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(summary.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	body := "## Goal\nbuild a thing"
	if err := os.WriteFile(summary.Path(dir, "S1"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/sessions/S1/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	got, _ := io.ReadAll(w.Result().Body)
	if string(got) != body {
		t.Errorf("body=%q, want %q", got, body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type=%q, want text/markdown", ct)
	}
}

func TestGetSummary_FileMissing_Returns404(t *testing.T) {
	dir := t.TempDir()
	req := httptest.NewRequest("GET", "/api/sessions/NOPE/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", w.Code)
	}
}

func TestGetSummary_EmptyFile_Returns200WithEmptyBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(summary.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(summary.Path(dir, "S2"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/sessions/S2/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (empty body is the frontend's responsibility)", w.Code)
	}
	got, _ := io.ReadAll(w.Result().Body)
	if len(got) != 0 {
		t.Errorf("body=%q, want empty", got)
	}
}

func TestGetSummary_PathTraversal_Returns404(t *testing.T) {
	dir := t.TempDir()
	// Try to escape the summaries/ dir. chi URLDecodes for us, but
	// even if a traversal sid slipped through, summary.Path uses
	// filepath.Join which would normalise — and our os.Open would
	// hit a nonexistent path on most layouts. We just assert 404.
	req := httptest.NewRequest("GET", "/api/sessions/..%2F..%2Fsessions.json/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404 for traversal attempt", w.Code)
	}
}

// Keep filepath import alive in case test grows.
var _ = filepath.Separator
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/api/...`
Expected: build failure ("undefined: GetSummaryHandler").

- [ ] **Step 3: Implement the handler**

Create `internal/api/summary_handler.go`:

```go
package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/summary"
)

// GetSummaryHandler serves the current contents of the session's
// summary file. 200 with the body on success (empty body is fine
// — the frontend renders the "no summary yet" state for both 404
// and empty 200). 404 when the file doesn't exist.
//
// Path traversal is bounded by enforcing that the resolved path
// stays under <dataDir>/summaries/. Any attempt to escape returns
// 404 (we never let an Open touch an unrelated path).
func GetSummaryHandler(dataDir string) http.Handler {
	root := summary.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		// Defence in depth: refuse anything with separators or
		// `..` segments in the sid. Real session IDs are ULIDs —
		// they never legitimately contain these characters.
		if strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
			writeError(w, http.StatusNotFound, "not_found", "no such summary")
			return
		}
		path := summary.Path(dataDir, sid)
		// Confirm the resolved abs path is still under root.
		clean := filepath.Clean(path)
		if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
			writeError(w, http.StatusNotFound, "not_found", "no such summary")
			return
		}
		body, err := os.ReadFile(clean)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "not_found", "no summary file")
				return
			}
			writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(body)
	})
}
```

- [ ] **Step 4: Wire the route**

Modify `internal/api/router.go`. Find the place where other `Deps`-style routes are wired (look for an existing pattern like `r.Get("/api/sessions/{sid}/commands", ...)`). Add:

```go
		r.Get("/api/sessions/{sid}/summary", GetSummaryHandler(d.Manager.DataDir()).ServeHTTP)
```

(`d.Manager` is the existing `Deps.Manager` field — same pattern as `commands` route.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/api/...`
Expected: PASS (4 new tests).

- [ ] **Step 6: Commit**

```bash
git add internal/api/summary_handler.go internal/api/summary_handler_test.go internal/api/router.go
git commit -m "feat(api): GET /api/sessions/{sid}/summary serves the on-disk summary file"
```

---

### Task 5: Integration smoke

- [ ] **Step 1: Run the whole Go test suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 2: Build the binary**

Run: `go build -o /tmp/alfred-server ./cmd/alfred-server`
Expected: clean build, binary in `/tmp`.

- [ ] **Step 3: Manual REST sanity**

Start the binary against a temp data dir, then poke the new endpoint:

```bash
mkdir -p /tmp/alfred-plan02/data/summaries
echo "## Goal\nverify" > /tmp/alfred-plan02/data/summaries/manual-test.md
ALFRED_USER=admin ALFRED_PASSWORD=admin ALFRED_TOKEN=devtoken \
  ALFRED_ADDR=:18080 ALFRED_DATA_DIR=/tmp/alfred-plan02/data \
  /tmp/alfred-server > /tmp/plan02.log 2>&1 &
sleep 1
curl -s -X POST http://127.0.0.1:18080/api/login \
  -H 'content-type: application/json' \
  -d '{"user":"admin","password":"admin"}'
# Take the token from the response; if it equals ALFRED_TOKEN, even better:
curl -s -i http://127.0.0.1:18080/api/sessions/manual-test/summary \
  -H "Authorization: Bearer devtoken"
# Expected: HTTP/1.1 200 OK, Content-Type: text/markdown; body == "## Goal\nverify\n"

# 404 for missing:
curl -s -i http://127.0.0.1:18080/api/sessions/no-such/summary \
  -H "Authorization: Bearer devtoken"
# Expected: HTTP/1.1 404 Not Found

kill %1
```

Expected: both responses as commented.

- [ ] **Step 4: Run the existing Playwright e2e suite**

Make sure alfred-server + Vite are running (you can leave the plan02 throwaway one running or restart the dev one), then:

```bash
cd web && npx playwright test --reporter=line
```

Expected: all existing tests still PASS. We haven't changed the WS or frontend yet, so nothing should regress.
