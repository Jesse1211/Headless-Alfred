# Summary template — Plan 01: Backend template system

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the server-side scaffolding for per-session prompt templates: a server-internal registry of one built-in (`summary-todo`), a per-session `TemplateID` field, prompt injection in `handleClaudePrompt`, and a `GET /api/templates/{id}` endpoint so the frontend can show the raw template text in a read-only viewer.

**Architecture:** New `internal/template/` package owns the static registry and rendering. `internal/store/sessions.go` gains a `TemplateID` field; `internal/session/manager.go` gains a getter/setter and Reconcile-clears-on-restart. `internal/api/claude_handlers.go::handleClaudePrompt` appends the rendered template to the user's prompt text. `internal/api/router.go` wires up the new HTTP route. Frontend wiring lives in plan 04.

**Tech Stack:** Go 1.25, `chi` for routing, `go test` for tests.

After this plan: backend can accept `templateId` in the `enter_claude` WS frame, persist it, and inject the rendered template on every subsequent `claude_prompt`. The summary file isn't read/written yet — that's plan 02. No frontend changes here.

---

### Task 1: Template registry — `internal/template/builtin.go`

**Files:**
- Create: `internal/template/builtin.go`
- Test: `internal/template/builtin_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/template/builtin_test.go`:

```go
package template

import "testing"

func TestBuiltins_SummaryTodoIsRegistered(t *testing.T) {
	t.Helper()
	tpl, ok := Builtins["summary-todo"]
	if !ok {
		t.Fatal("Builtins[summary-todo] missing")
	}
	if tpl.ID != "summary-todo" {
		t.Errorf("ID=%q want summary-todo", tpl.ID)
	}
	if tpl.Name == "" {
		t.Error("Name must be non-empty")
	}
	if len(tpl.Content) < 100 {
		t.Errorf("Content too short (%d bytes); expected the full task-list instructions", len(tpl.Content))
	}
	// The two placeholders we'll substitute later must appear verbatim.
	for _, marker := range []string{"<sid>", "<summary_path>"} {
		if !contains(tpl.Content, marker) {
			t.Errorf("Content missing placeholder %q", marker)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/template/...`
Expected: build failure ("no Go files in internal/template/").

- [ ] **Step 3: Implement the registry**

Create `internal/template/builtin.go`:

```go
// Package template owns the server-side registry of prompt
// templates that get appended to user prompts when a session opts
// in via the Start Claude dialog. Templates are server-side
// constants for v1 — there is no UI for the user to author or
// edit them. New built-ins are added to Builtins in code.
package template

// Template is one entry in the registry.
type Template struct {
	// ID is the stable key used in URLs, WS frames, and on disk
	// (SessionMeta.TemplateID). Kebab-case; URL-safe.
	ID string

	// Name is the human-readable label shown in the UI (read-only
	// viewer).
	Name string

	// Content is the raw template text. Render() substitutes the
	// per-session placeholders before it is appended to the user's
	// prompt.
	//
	// Supported placeholders:
	//   <sid>            — the Alfred session ID
	//   <summary_path>   — the absolute path to the session's
	//                      summary file on disk
	Content string
}

// Builtins is the registry. Keys equal Template.ID.
//
// summary-todo: the v1 default. Asks Claude to maintain a short
// task-list summary in the session's summary file. The user reads
// it in the right-hand sidebar; updates happen by Claude's Read +
// Write tools.
var Builtins = map[string]Template{
	"summary-todo": {
		ID:   "summary-todo",
		Name: "Task summary",
		Content: `After your reply, update the session summary at
<summary_path> so we don't lose context across turns.

Steps:

1. Use Read on <summary_path> first if it exists; preserve
   still-relevant content, remove obsolete items.
2. Rewrite the whole file in this shape (keep the WHOLE file
   under 1500 characters — short bullets, no narrative
   paragraphs, no verbatim code blocks):

## Goal
<one line: what we're trying to achieve>

## Status
<one line: in progress / blocked on X / done>

## Decisions
- <terse bullets of what we've agreed on>

## Open questions
- <things still unresolved>

3. Use Write (one tool call, full file contents).

Session id: <sid>.
`,
	},
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/template/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/template/builtin.go internal/template/builtin_test.go
git commit -m "feat(template): registry of built-in prompt templates with summary-todo"
```

---

### Task 2: Render — `internal/template/render.go`

**Files:**
- Create: `internal/template/render.go`
- Test: `internal/template/render_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/template/render_test.go`:

```go
package template

import (
	"strings"
	"testing"
)

func TestRender_SubstitutesPlaceholders(t *testing.T) {
	out := Render("summary-todo", "01KSESSION", "/data/summaries/01KSESSION.md")
	if out == "" {
		t.Fatal("Render returned empty for known id")
	}
	if !strings.Contains(out, "01KSESSION") {
		t.Error("Render did not substitute <sid>")
	}
	if !strings.Contains(out, "/data/summaries/01KSESSION.md") {
		t.Error("Render did not substitute <summary_path>")
	}
	if strings.Contains(out, "<sid>") || strings.Contains(out, "<summary_path>") {
		t.Errorf("Render left placeholder unsubstituted: %s", out)
	}
}

func TestRender_UnknownIDReturnsEmpty(t *testing.T) {
	if got := Render("does-not-exist", "X", "/x"); got != "" {
		t.Errorf("Render(unknown)=%q, want \"\"", got)
	}
}

func TestRender_EmptyIDReturnsEmpty(t *testing.T) {
	if got := Render("", "X", "/x"); got != "" {
		t.Errorf("Render(\"\")=%q, want \"\"", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/template/...`
Expected: build failure ("undefined: Render").

- [ ] **Step 3: Implement Render**

Create `internal/template/render.go`:

```go
package template

import "strings"

// Render substitutes the per-session placeholders in the template
// and returns the result. Returns "" for unknown or empty id —
// callers treat that as "no template, skip injection".
func Render(id, sessionID, summaryPath string) string {
	t, ok := Builtins[id]
	if !ok {
		return ""
	}
	s := t.Content
	s = strings.ReplaceAll(s, "<sid>", sessionID)
	s = strings.ReplaceAll(s, "<summary_path>", summaryPath)
	return s
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/template/...`
Expected: PASS (4 tests total).

- [ ] **Step 5: Commit**

```bash
git add internal/template/render.go internal/template/render_test.go
git commit -m "feat(template): Render substitutes <sid> + <summary_path> placeholders"
```

---

### Task 3: SessionMeta.TemplateID — `internal/store/sessions.go`

**Files:**
- Modify: `internal/store/sessions.go` (add field to `SessionMeta`)

- [ ] **Step 1: Write the failing test**

Create or extend `internal/store/sessions_template_test.go`:

```go
package store

import (
	"encoding/json"
	"testing"
)

func TestSessionMeta_TemplateID_Roundtrip(t *testing.T) {
	in := SessionMeta{ID: "X", Name: "n", TemplateID: "summary-todo"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionMeta
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.TemplateID != "summary-todo" {
		t.Errorf("TemplateID=%q after roundtrip, want summary-todo", out.TemplateID)
	}
}

func TestSessionMeta_TemplateID_OmittedWhenEmpty(t *testing.T) {
	in := SessionMeta{ID: "X", Name: "n"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); contains(got, `"template_id"`) {
		t.Errorf("expected template_id to be omitted when empty; got %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/...`
Expected: build failure ("unknown field TemplateID").

- [ ] **Step 3: Add the field**

Modify `internal/store/sessions.go`. Find the existing `SessionMeta` struct (currently has `ID`, `Name`, `CreatedAt`, `Mode`, `Renderer`, `ClaudeSessionID`, `ClaudeBypassPermissions`). After `ClaudeBypassPermissions`, add:

```go
	// TemplateID names the active prompt template (key into
	// internal/template.Builtins), e.g. "summary-todo". Empty = no
	// template active; handleClaudePrompt skips injection. Set on
	// enter_claude based on the dialog checkbox; cleared by
	// Manager.Reconcile on Pod restart along with Renderer.
	TemplateID string `json:"template_id,omitempty"`
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/store/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sessions.go internal/store/sessions_template_test.go
git commit -m "feat(store): SessionMeta.TemplateID field"
```

---

### Task 4: Manager getters/setters + Reconcile-clear — `internal/session/manager.go`

**Files:**
- Modify: `internal/session/manager.go` (add `GetTemplateID`, `SetTemplateID`, clear on Reconcile)
- Test: `internal/session/template_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/session/template_test.go`:

```go
package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/store"
)

// We reuse the existing test helper that builds an in-memory manager.
// If the helper is named differently in this codebase, adjust the
// call below — keep the rest identical.
func TestManager_TemplateID_SetGet(t *testing.T) {
	m := newTestManager(t)
	sid := mustCreate(t, m, "S1")

	if got := m.GetTemplateID(sid); got != "" {
		t.Errorf("default GetTemplateID=%q, want empty", got)
	}
	if err := m.SetTemplateID(sid, "summary-todo"); err != nil {
		t.Fatal(err)
	}
	if got := m.GetTemplateID(sid); got != "summary-todo" {
		t.Errorf("after SetTemplateID, GetTemplateID=%q, want summary-todo", got)
	}
}

func TestManager_TemplateID_PersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	m1 := newTestManagerInDir(t, dir)
	sid := mustCreate(t, m1, "S1")
	if err := m1.SetTemplateID(sid, "summary-todo"); err != nil {
		t.Fatal(err)
	}
	// Read sessions.json directly; SetTemplateID must have persisted.
	data := mustReadFile(t, filepath.Join(dir, "sessions.json"))
	if !bytesContains(data, "summary-todo") {
		t.Errorf("sessions.json missing 'summary-todo' after SetTemplateID")
	}
}

func TestManager_Reconcile_ClearsTemplateID(t *testing.T) {
	dir := t.TempDir()
	m1 := newTestManagerInDir(t, dir)
	sid := mustCreate(t, m1, "S1")
	if err := m1.SetTemplateID(sid, "summary-todo"); err != nil {
		t.Fatal(err)
	}
	// Simulate Pod restart: tear down + rebuild from disk, then
	// Reconcile. After Reconcile, TemplateID should be cleared
	// (parallel to Renderer being cleared).
	m1.Close(context.Background())
	m2 := newTestManagerInDir(t, dir)
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := m2.GetTemplateID(sid); got != "" {
		t.Errorf("after Reconcile, TemplateID=%q, want empty", got)
	}
	_ = store.RendererTUI // import keep
}
```

Note: this assumes test helpers `newTestManager`, `newTestManagerInDir`, `mustCreate`, `mustReadFile`, and `bytesContains` already exist in this package (we use them for Renderer tests). If your local helper names differ, mirror what the existing `renderer_test.go` uses.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/session/...`
Expected: build failure ("undefined: GetTemplateID" / "undefined: SetTemplateID").

- [ ] **Step 3: Add methods + Reconcile-clear**

Modify `internal/session/manager.go`.

Find the existing block defining `GetRenderer` / `SetRenderer`. Immediately after `SetRenderer`, add:

```go
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
```

Find `Reconcile`'s "stored \ live" branch that already calls `SetRenderer(meta.ID, "")` and `SetClaudeBypass(meta.ID, false)`. Add a third clear right after:

```go
		// Same for the template id — it's an entry-time choice and
		// the per-session summary file (if any) on disk is what
		// remembers across restarts, not the template flag.
		if err := m.SetTemplateID(meta.ID, ""); err != nil && !errors.Is(err, ErrSessionNotFound) {
			m.cfg.Logger.Error("reset template id after recreate", "session", meta.ID, "err", err)
		}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/session/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/manager.go internal/session/template_test.go
git commit -m "feat(session): TemplateID getter/setter + clear on Reconcile"
```

---

### Task 5: enter_claude carries templateId + handleClaudePrompt injects

**Files:**
- Modify: `internal/api/wsproto.go` (add `TemplateID` field to `InMsg`)
- Modify: `internal/api/claude_handlers.go` (handleEnterClaude: SetTemplateID; handleClaudePrompt: append rendered template)
- Modify: `internal/session/manager.go` (add `DataDir()` accessor)
- Test: `internal/api/claude_prompt_template_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/api/claude_prompt_template_test.go`:

```go
package api

import (
	"strings"
	"testing"
)

// Verifies the helper that builds the final prompt text injected
// into `claude -p`. We isolate the pure function so we don't have
// to spin up a real manager + runner in this unit test.
func TestComposePromptText_NoTemplate_PassThrough(t *testing.T) {
	got := composePromptText("hello", "" /*id*/, "01SID", "/data/summaries/01SID.md")
	if got != "hello" {
		t.Errorf("composePromptText=%q, want %q", got, "hello")
	}
}

func TestComposePromptText_WithTemplate_Appended(t *testing.T) {
	got := composePromptText("hello", "summary-todo", "01SID", "/data/summaries/01SID.md")
	if !strings.HasPrefix(got, "hello\n\n---\n") {
		t.Errorf("template not appended after delimiter; got prefix %q", got[:min(60, len(got))])
	}
	if !strings.Contains(got, "/data/summaries/01SID.md") {
		t.Error("rendered template missing summary path substitution")
	}
	if !strings.Contains(got, "01SID") {
		t.Error("rendered template missing sid substitution")
	}
}

func TestComposePromptText_UnknownTemplate_PassThrough(t *testing.T) {
	got := composePromptText("hello", "no-such", "01SID", "/data/summaries/01SID.md")
	if got != "hello" {
		t.Errorf("composePromptText(unknown id)=%q, want passthrough", got)
	}
}

func min(a, b int) int { if a < b { return a }; return b }
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/api/...`
Expected: build failure ("undefined: composePromptText").

- [ ] **Step 3: Add TemplateID to InMsg**

Modify `internal/api/wsproto.go`. In the `InMsg` struct, in the V1 claude UI block, add a new field after `Renderer`:

```go
	TemplateID         string `json:"templateId,omitempty"`         // enter_claude: which template to attach to the session
```

- [ ] **Step 4: Add DataDir accessor on Manager**

Modify `internal/session/manager.go`. After the constructor / next to other tiny accessors (look for `GetMode`), add:

```go
// DataDir returns the root data directory passed in Config.DataDir.
// Used by callers that need to compute on-disk paths (e.g. the
// per-session summary file).
func (m *Manager) DataDir() string {
	return m.cfg.DataDir
}
```

- [ ] **Step 5: Add composePromptText + wire it in**

Modify `internal/api/claude_handlers.go`.

At the top of the file (after imports), add the helper:

```go
// composePromptText assembles the final prompt text that will be
// piped to `claude -p` stdin. If the session has a template
// active, the rendered template is appended after the user's
// message separated by a markdown horizontal rule so Claude can
// tell what came from the user and what came from the harness.
func composePromptText(userText, templateID, sessionID, summaryPath string) string {
	rendered := template.Render(templateID, sessionID, summaryPath)
	if rendered == "" {
		return userText
	}
	return userText + "\n\n---\n" + rendered
}
```

Add the import (top of file):

```go
	"github.com/jesseliu/headless-alfred/internal/template"
	"github.com/jesseliu/headless-alfred/internal/summary" // added; will live in plan 02 — for now create a minimal stub
```

Stub `internal/summary/path.go` so this plan compiles (plan 02 reuses + tests it):

Create `internal/summary/path.go`:

```go
// Package summary owns the per-session summary file: its on-disk
// path, the watcher that notices writes to it, and helpers shared
// by the prompt-injection path and the HTTP handler.
package summary

import "path/filepath"

// Path returns the on-disk summary path for the session.
// <dataDir>/summaries/<sessionID>.md
func Path(dataDir, sessionID string) string {
	return filepath.Join(dataDir, "summaries", sessionID+".md")
}
```

Find `handleEnterClaude`. After the existing `SetClaudeBypass` call (added in a previous commit), add:

```go
	if err := m.SetTemplateID(msg.SessionID, msg.TemplateID); err != nil {
		slog.Warn("SetTemplateID failed", "session", msg.SessionID, "err", err)
	}
```

Find `handleClaudePrompt`. The current body wraps `msg.Text` directly. Replace the line that passes `Prompt: msg.Text` to `runner.Prompt` with a small composition step. Concretely, find this section (the exact existing wording around the runner.Prompt call):

```go
	pr, err := runner.Prompt(ctx, claude.PromptOptions{
		SessionUUID:       convoID,
		CWD:               cwd,
		Prompt:            msg.Text,
		BypassPermissions: m.GetClaudeBypass(msg.SessionID),
	})
```

Replace with:

```go
	finalText := composePromptText(
		msg.Text,
		m.GetTemplateID(msg.SessionID),
		msg.SessionID,
		summary.Path(m.DataDir(), msg.SessionID),
	)
	pr, err := runner.Prompt(ctx, claude.PromptOptions{
		SessionUUID:       convoID,
		CWD:               cwd,
		Prompt:            finalText,
		BypassPermissions: m.GetClaudeBypass(msg.SessionID),
	})
```

- [ ] **Step 6: Run all tests to verify everything passes**

Run: `go test ./internal/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/api/wsproto.go internal/api/claude_handlers.go \
        internal/api/claude_prompt_template_test.go \
        internal/session/manager.go \
        internal/summary/path.go
git commit -m "feat(api): wire templateId through enter_claude + inject template into claude_prompt"
```

---

### Task 6: HTTP `GET /api/templates/{id}`

**Files:**
- Modify: `internal/api/router.go` (add route)
- Create: `internal/api/templates_handler.go`
- Test: `internal/api/templates_handler_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/api/templates_handler_test.go`:

```go
package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetTemplate_Known_Returns200WithBody(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/templates/summary-todo", nil)
	w := httptest.NewRecorder()
	GetTemplateHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "<sid>") || !strings.Contains(string(body), "<summary_path>") {
		t.Errorf("body missing placeholders (we serve the raw, un-substituted template)")
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type=%q, want text/plain; charset=utf-8", got)
	}
}

func TestGetTemplate_Unknown_Returns404(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/templates/does-not-exist", nil)
	w := httptest.NewRecorder()
	GetTemplateHandler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", w.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/api/...`
Expected: build failure ("undefined: GetTemplateHandler").

- [ ] **Step 3: Implement handler**

Create `internal/api/templates_handler.go`:

```go
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/template"
)

// GetTemplateHandler serves the raw, un-substituted template
// content so the frontend can show the user what gets injected.
// Read-only — there is no PUT/POST counterpart.
func GetTemplateHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		tpl, ok := template.Builtins[id]
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such template")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(tpl.Content))
	})
}
```

- [ ] **Step 4: Wire the route**

Modify `internal/api/router.go`. Find the block that registers other auth'd GET routes (e.g. the existing `/api/sessions/...` GET). Add immediately after:

```go
		r.Get("/api/templates/{id}", GetTemplateHandler().ServeHTTP)
```

If the file uses a `Deps` struct that's passed to other handlers, `GetTemplateHandler` deliberately needs no Deps — the registry is package-global.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/api/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/templates_handler.go internal/api/templates_handler_test.go internal/api/router.go
git commit -m "feat(api): GET /api/templates/{id} serves raw template text"
```

---

### Final integration check

- [ ] **Step 1: Run the whole Go test suite + vet**

Run:
```bash
go test ./...
go vet ./...
```

Expected: all PASS, no vet warnings introduced.

- [ ] **Step 2: Run the existing Playwright e2e to confirm no regression**

Make sure alfred-server + Vite are running, then:

```bash
cd web && npx playwright test --reporter=line
```

Expected: all existing tests still PASS (we changed protocol but the frontend hasn't started sending `templateId` yet — old enter_claude frames still work; empty TemplateID = no injection).

- [ ] **Step 3: Hand-smoke via WS probe**

Run the existing `scripts/claude-ui-smoke`:

```bash
go run ./scripts/claude-ui-smoke
```

Expected: PASS. The smoke doesn't exercise the new template path (no templateId sent), so it should be a no-op for our additions.
