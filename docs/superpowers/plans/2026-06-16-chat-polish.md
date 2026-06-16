# Chat & Sidebar Polish (3 Features) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Three independent UX improvements bundled in one plan: (A) collapsible tool-call display in Claude chat, (B) header Summary toggle becomes an icon, (C) per-session Notes panel added alongside Summary in the right sidebar (Notes is user-only — never sent to Claude).

**Architecture:** A and B are pure frontend. C mirrors the existing `internal/summary` package as `internal/notes` (path + watcher + handler), plus **2** new HTTP endpoints (GET + PUT note), one new WS frame `note_updated`, one new `NotesPanel` React component, and a small refactor of `SummarySidebar` into a generic right-rail container that accordion-stacks Summary + Notes when both are eligible. Recap sessions intentionally keep their dedicated `RecapSidebar` and do NOT get a Notes panel — recap sessions are ephemeral by design, scribbling personal notes there has no good destination on disk.

**Sidebar visibility key migration:** The toggle's localStorage key is renamed from `alfred_summary_sidebar_hidden` to `alfred_right_sidebar_hidden` because the right rail is no longer summary-only. On read, the new code checks the new key first and falls back to the old key (one-time migration) so a returning user keeps their hidden/shown preference.

**Tech Stack:** Go 1.25, fsnotify v1.7.0, TypeScript, React 18, Vitest, Playwright (existing).

---

## File Structure

| file | role | which feature |
|---|---|---|
| `web/src/features/sessions/ClaudeChatView.tsx` | `ToolCallView` becomes default-collapsed with click-to-expand | A |
| `web/src/features/sessions/ClaudeChatView.css` | styles for the collapsed/expanded tool row | A |
| `web/src/features/sessions/WorkspacePage.tsx` | Summary text button → SVG icon button; sidebar visibility predicate broadens to include Notes-only case | B + C |
| `web/src/features/sessions/WorkspacePage.css` | icon button styling | B |
| `internal/notes/path.go` | new — `Dir(dataDir)`, `Path(dataDir, sid)` | C |
| `internal/notes/watcher.go` | new — fsnotify watcher mirroring `internal/summary/watcher.go` | C |
| `internal/notes/*_test.go` | unit tests for path + watcher | C |
| `internal/api/notes_handler.go` | new — GET/PUT `/api/sessions/{sid}/note` | C |
| `internal/api/notes_handler_test.go` | tests for the handler | C |
| `internal/api/wsproto.go` | `TypeNoteUpdated` const + Date already reused; nothing else | C |
| `internal/api/router.go` | wire 2 new routes | C |
| `internal/api/ws.go` | per-WS note watcher subscription (mirrors summary path) | C |
| `cmd/alfred-server/main.go` | start the global notes watcher | C |
| `web/src/lib/api.ts` | `getNote`, `putNote` helpers | C |
| `web/src/lib/ws.ts` | `note_updated` ServerMsg variant | C |
| `web/src/features/sessions/types.ts` | `noteFetchCounter?: number` on `PerSessionState` | C |
| `web/src/features/sessions/sessionsReducer.ts` | `note_updated` bumps counter | C |
| `web/src/features/sessions/NotesPanel.tsx` | new — editable textarea, debounced save | C |
| `web/src/features/sessions/NotesPanel.css` | styles | C |
| `web/src/features/sessions/RightRail.tsx` | new — accordion container that stacks Summary + Notes | C |
| `web/src/features/sessions/RightRail.css` | accordion styling | C |
| `web/src/features/sessions/SummarySidebar.tsx` | refactor: drop outer `<aside>` wrapper, become a content component the accordion renders | C |

---

## Part A: Collapsible Tool Calls

### Task A1: Default-collapse `ToolCallView`, add expand toggle

**Files:**
- Modify: `web/src/features/sessions/ClaudeChatView.tsx`
- Modify: `web/src/features/sessions/ClaudeChatView.css`

Goal: tool rows are visually one line by default (`Bash(cd $(pwd) && git log…)`), click anywhere on the row to expand the input + result.

- [ ] **Step 1: Add per-tool collapsed state in ToolCallView**

Find the existing `ToolCallView` (currently around line 226). Replace its body with a collapsible shape:

```tsx
function ToolCallView({ tool }: { tool: ClaudeToolCall }) {
  const [expanded, setExpanded] = useState(false)
  const status = toolStatus(tool)
  // Build a one-line preview of the first input field for the
  // collapsed header. Bash → command; Read/Write/Edit → file_path;
  // anything else → first JSON value. Truncate to 80 chars.
  const preview = toolPreview(tool)
  const hasDetails = tool.input != null || (tool.result != null && tool.result !== '')
  return (
    <div className={`claude-tool claude-tool--${status} ${tool.isError ? 'is-error' : ''} ${expanded ? 'is-expanded' : ''}`}>
      <button
        type="button"
        className="claude-tool__row"
        onClick={() => hasDetails && setExpanded((v) => !v)}
        title={hasDetails ? (expanded ? 'Collapse' : 'Expand') : ''}
        aria-expanded={expanded}
      >
        <span className="claude-tool__chev">{hasDetails ? (expanded ? '▾' : '▸') : '·'}</span>
        <code className="claude-tool__name">{tool.name}</code>
        {preview && <span className="claude-tool__preview">({preview})</span>}
        <span className="claude-tool__status">{status}</span>
      </button>
      {expanded && hasDetails && (
        <div className="claude-tool__body">
          {tool.input != null && (
            <pre className="claude-tool__input">{formatJSON(tool.input)}</pre>
          )}
          {tool.result != null && tool.result !== '' && (
            <pre className="claude-tool__result">{tool.result}</pre>
          )}
        </div>
      )}
    </div>
  )
}

// toolPreview returns a short, human-readable summary of the tool's
// principal argument so the collapsed row reads naturally.
// Bash → first 80 chars of command; Read/Write/Edit/Glob/Grep →
// file_path/pattern; any other → first string-valued top-level field.
function toolPreview(tool: ClaudeToolCall): string {
  const inp = tool.input as Record<string, unknown> | null
  if (!inp || typeof inp !== 'object') return ''
  const pick = (key: string): string | null => {
    const v = inp[key]
    return typeof v === 'string' ? v : null
  }
  const candidates = ['command', 'file_path', 'path', 'pattern', 'query', 'url']
  for (const k of candidates) {
    const v = pick(k)
    if (v) return v.length > 80 ? v.slice(0, 77) + '…' : v
  }
  // Fallback: first string field of any name.
  for (const v of Object.values(inp)) {
    if (typeof v === 'string' && v) return v.length > 80 ? v.slice(0, 77) + '…' : v
  }
  return ''
}
```

(`useState` is already imported at the top of the file; verify before editing.)

- [ ] **Step 2: Restyle in CSS — collapsed = one row, expanded = stacked**

Append to `web/src/features/sessions/ClaudeChatView.css`:

```css
.claude-tool {
  margin: 6px 0;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 6px;
  background: rgba(255, 255, 255, 0.02);
  overflow: hidden;
}

.claude-tool__row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 10px;
  background: transparent;
  border: none;
  color: var(--text, #eaeaea);
  cursor: pointer;
  text-align: left;
  font-size: 12px;
}

.claude-tool__row:hover {
  background: rgba(255, 255, 255, 0.04);
}

.claude-tool__chev {
  width: 12px;
  color: var(--text-muted, #9aa3b2);
  font-size: 10px;
  flex-shrink: 0;
}

.claude-tool__name {
  font-weight: 600;
  font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11.5px;
}

.claude-tool__preview {
  color: var(--text-muted, #9aa3b2);
  font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.claude-tool__status {
  flex-shrink: 0;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted, #9aa3b2);
}

.claude-tool--done .claude-tool__status { color: #7ed492; }
.claude-tool--denied .claude-tool__status { color: #e88c8c; }
.claude-tool--running .claude-tool__status { color: #7aa2f7; }
.claude-tool--pending .claude-tool__status { color: #e0c97a; }
.claude-tool.is-error .claude-tool__status { color: #e88c8c; }

.claude-tool__body {
  padding: 0 10px 10px 30px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
}

.claude-tool__input,
.claude-tool__result {
  margin: 8px 0 0;
  padding: 8px 10px;
  background: rgba(0, 0, 0, 0.25);
  border-radius: 4px;
  font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 360px;
  overflow-y: auto;
  color: var(--text, #eaeaea);
}

.claude-tool__result {
  background: rgba(0, 0, 0, 0.4);
}
```

If the old `.claude-tool__*` rules already exist in this file (the previous design), DELETE them first to avoid conflicts. Search for `.claude-tool` and clear out anything that isn't in the block above.

- [ ] **Step 3: TS + tests**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && npx tsc --noEmit && npm test -- --run 2>&1 | tail -4
```

Expected: TS clean, all existing tests still pass.

- [ ] **Step 4: Manual visual smoke**

Open a Claude UI session in the browser and send a prompt that triggers a tool. Quickest path is to pick a session that already has past turns with tools — the jsonl-restore loader populates them on reload and you see the new compact rows immediately, no need to wait for a fresh Claude run (which takes 30-60s on a real API call).

Confirm:
- Tool row shows `▸ Bash (git status) done` on one line
- Click the row → it expands, showing the input JSON + result pre blocks
- Click again → collapses
- The status pill is colour-coded per the CSS

- [ ] **Step 5: Commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
git add web/src/features/sessions/ClaudeChatView.tsx web/src/features/sessions/ClaudeChatView.css && \
git commit -m "feat(chat): collapsible tool-call rows in Claude chat view"
```

---

## Part B: Summary Button → Icon

### Task B1: Replace `Summary` text button with an SVG icon

**Files:**
- Modify: `web/src/features/sessions/WorkspacePage.tsx`
- Modify: `web/src/features/sessions/WorkspacePage.css`

- [ ] **Step 1: Swap the button content**

Find the existing block in `WorkspacePage.tsx` (currently around line 200):

```tsx
{selected && ps && ps.mode === 'claude' && ps.templateId === 'summary-todo' && !isRecap && (
  <button
    type="button"
    className={`workspace__summary-btn ${sidebarHidden ? '' : 'is-active'}`}
    onClick={() => setSidebarHiddenPersisted(!sidebarHidden)}
    title={sidebarHidden ? 'Show summary sidebar' : 'Hide summary sidebar'}
    aria-pressed={!sidebarHidden}
  >
    Summary
  </button>
)}
```

Replace with the same wrapper + an SVG sidebar icon (no text):

```tsx
{selected && ps && !isRecap && (
  <button
    type="button"
    className={`workspace__sidebar-icon-btn ${sidebarHidden ? '' : 'is-active'}`}
    onClick={() => setSidebarHiddenPersisted(!sidebarHidden)}
    title={sidebarHidden ? 'Show right sidebar' : 'Hide right sidebar'}
    aria-pressed={!sidebarHidden}
    aria-label="Toggle right sidebar"
  >
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      {/* Outer frame */}
      <rect x="1.5" y="2.5" width="13" height="11" rx="1.5" stroke="currentColor" strokeWidth="1.3" />
      {/* Right-pane divider */}
      <line x1="10" y1="2.5" x2="10" y2="13.5" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  </button>
)}
```

Predicate notes:
- The visibility check is `!isRecap` (drops both the `mode === 'claude'` requirement AND the `templateId === 'summary-todo'` requirement). Part C makes the right rail useful in every non-recap session, including shell mode, because Notes always shows up. **The icon predicate MUST match `showRightRail` in Part C exactly** — if shell sessions can show the rail but not the icon, the user has no way to re-open the rail after hiding it.
- Recap sessions still get the RecapSidebar (a separate component), so the icon is correctly hidden there.
- The CSS class name changes from `workspace__summary-btn` to `workspace__sidebar-icon-btn` to reflect the broader role.

Also rename the localStorage key (one-time migration: read new key first, fall back to old):

Find the existing definition in `WorkspacePage.tsx` (search for `alfred_summary_sidebar_hidden`):

```tsx
const [sidebarHidden, setSidebarHidden] = useState<boolean>(() => {
  try { return localStorage.getItem('alfred_summary_sidebar_hidden') === '1' } catch { return false }
})
```

Replace with:

```tsx
const [sidebarHidden, setSidebarHidden] = useState<boolean>(() => {
  try {
    // Prefer the new key. Fall back to the old summary-only key
    // for a one-time migration so returning users keep their
    // hide preference.
    const v = localStorage.getItem('alfred_right_sidebar_hidden')
    if (v !== null) return v === '1'
    return localStorage.getItem('alfred_summary_sidebar_hidden') === '1'
  } catch { return false }
})
```

And in `setSidebarHiddenPersisted`:

```tsx
const setSidebarHiddenPersisted = useCallback((hidden: boolean) => {
  setSidebarHidden(hidden)
  try {
    localStorage.setItem('alfred_right_sidebar_hidden', hidden ? '1' : '0')
    localStorage.removeItem('alfred_summary_sidebar_hidden') // migrate
  } catch { /* localStorage unavailable */ }
}, [])
```

- [ ] **Step 2: Restyle in CSS**

Open `web/src/features/sessions/WorkspacePage.css`. Find the existing `.workspace__summary-btn` rules and DELETE them (search the file; should be a handful of lines). Then append:

```css
.workspace__sidebar-icon-btn {
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 4px 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted, #9aa3b2);
  cursor: pointer;
  transition: color 0.12s ease, background 0.12s ease, border-color 0.12s ease;
}

.workspace__sidebar-icon-btn:hover {
  color: var(--text, #eaeaea);
  background: rgba(255, 255, 255, 0.04);
  border-color: rgba(255, 255, 255, 0.18);
}

.workspace__sidebar-icon-btn.is-active {
  color: var(--accent, #7aa2f7);
  border-color: var(--accent, #7aa2f7);
}
```

- [ ] **Step 3: TS + tests**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && npx tsc --noEmit && npm test -- --run 2>&1 | tail -4
```

Expected: clean + green.

- [ ] **Step 4: Manual smoke**

Open the app, enter Claude UI mode. Confirm the header shows a small "sidebar" icon (rectangle with a vertical line on the right ⅔) where "Summary" text used to be. Click it: sidebar collapses. Click again: re-shows. The accent border indicates the active (shown) state.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
git add web/src/features/sessions/WorkspacePage.tsx web/src/features/sessions/WorkspacePage.css && \
git commit -m "feat(header): Summary text button -> sidebar icon button"
```

---

## Part C: Notes Panel

This is the bigger feature. 9 tasks: 4 backend + 5 frontend.

### Task C1: `internal/notes` package (path + watcher + tests)

**Files:**
- Create: `internal/notes/path.go`
- Create: `internal/notes/path_test.go`
- Create: `internal/notes/watcher.go`
- Create: `internal/notes/watcher_test.go`

Mirror of `internal/summary` exactly. Different filename pattern (notes are per-session, same `<sid>.md` shape), different directory (`notes` instead of `summaries`).

- [ ] **Step 1: Write path.go**

```go
// Package notes owns the per-session notes file: its on-disk path,
// the watcher that notices writes, and helpers shared by the HTTP
// handler. Notes are user-authored only — NEVER injected into a
// Claude prompt; this package has no read path into composePromptText.
package notes

import "path/filepath"

// Path returns the on-disk notes path for the session.
// <dataDir>/notes/<sessionID>.md
func Path(dataDir, sessionID string) string {
	return filepath.Join(Dir(dataDir), sessionID+".md")
}

// Dir returns the directory holding all notes files. Used by the
// fsnotify watcher and by tests that seed files.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "notes")
}
```

- [ ] **Step 2: Write path_test.go**

```go
package notes

import (
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	got := Path("/data", "sid-A")
	want := filepath.Join("/data", "notes", "sid-A.md")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestDir(t *testing.T) {
	got := Dir("/data")
	want := filepath.Join("/data", "notes")
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}
```

- [ ] **Step 3: Write watcher.go**

Copy `internal/summary/watcher.go` and adapt: package name → `notes`, error message prefix → `notes.StartWatcher`, parser function → `parseNotesFilename` (same shape — extract `<sid>` from `<sid>.md`).

```go
package notes

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

// Watcher tails <dataDir>/notes/ and invokes onWrite(sid) whenever
// a <sid>.md file is created or modified. Per-file 200ms debounce.
type Watcher struct {
	w       *fsnotify.Watcher
	onWrite func(sessionID string)
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	pending map[string]*time.Timer
}

const debounce = 200 * time.Millisecond

func StartWatcher(dataDir string, onWrite func(sessionID string)) (*Watcher, error) {
	if onWrite == nil {
		return nil, errors.New("notes.StartWatcher: onWrite required")
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

func (w *Watcher) Stop() {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	for _, t := range w.pending {
		t.Stop()
	}
	w.pending = map[string]*time.Timer{}
	w.mu.Unlock()
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
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			sid, ok := parseNotesFilename(filepath.Base(ev.Name))
			if !ok {
				continue
			}
			w.schedule(sid)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn("notes watcher", "err", err)
		}
	}
}

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

// parseNotesFilename returns (sid, true) for <sid>.md, skipping
// dotfiles and non-md.
func parseNotesFilename(name string) (string, bool) {
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

- [ ] **Step 4: Write watcher_test.go**

```go
package notes

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcher_FiresOnWriteWithSID(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var sids []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		sids = append(sids, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	path := filepath.Join(Dir(dir), "sid-A.md")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sids)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sids) == 0 || sids[0] != "sid-A" {
		t.Errorf("got %v, want [sid-A]", sids)
	}
}

func TestWatcher_SkipsDotfilesAndNonMd(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var fired int
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		fired++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	for _, name := range []string{".hidden.md", "no-ext", "with.txt"} {
		_ = os.WriteFile(filepath.Join(Dir(dir), name), []byte("x"), 0o644)
	}
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("fired = %d, want 0", fired)
	}
}
```

- [ ] **Step 5: Run + race**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred
go test ./internal/notes/...
go test -race ./internal/notes/...
```

Expected: PASS both.

- [ ] **Step 6: Commit**

```bash
git add internal/notes/
git commit -m "feat(notes): path + fsnotify watcher mirror of internal/summary"
```

---

### Task C2: `GET` and `PUT /api/sessions/{sid}/note` handlers

**Files:**
- Create: `internal/api/notes_handler.go`
- Create: `internal/api/notes_handler_test.go`

GET returns the body (404 when missing — frontend treats as empty). PUT writes the body atomically (tmp + rename) and caps at 64KB.

- [ ] **Step 1: Write failing tests**

```go
// internal/api/notes_handler_test.go
package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestGetNote_Missing_Returns404(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/note", GetNoteHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/sid-A/note", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

func TestGetNote_Existing_ReturnsBody(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
	body := "remember to test the recap edge case"
	_ = os.WriteFile(filepath.Join(dir, "notes", "sid-A.md"), []byte(body), 0o644)

	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/note", GetNoteHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/sid-A/note", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != body {
		t.Errorf("body = %q, want %q", w.Body.String(), body)
	}
}

func TestPutNote_WritesBody(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Put("/api/sessions/{sid}/note", PutNoteHandler(dir).ServeHTTP)
	body := "first note"
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/sessions/sid-A/note", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/markdown")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "notes", "sid-A.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("got %q want %q", string(got), body)
	}
}

func TestPutNote_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Put("/api/sessions/{sid}/note", PutNoteHandler(dir).ServeHTTP)
	huge := strings.Repeat("x", 64*1024+1)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/sessions/sid-A/note", strings.NewReader(huge))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 413 or 400", w.Code)
	}
}

func TestPutNote_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Put("/api/sessions/{sid}/note", PutNoteHandler(dir).ServeHTTP)
	// chi routing strips slashes by default; the explicit guard
	// inside the handler is what we're testing here. Force a
	// suspicious sid via a request URL chi will pass through.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/sessions/..%2Fhacked/note", strings.NewReader("x"))
	r.ServeHTTP(w, req)
	// 404 (chi may not match) OR 400 (handler refused) — both fine,
	// what we MUST NOT see is 204 with a file outside notes/.
	if w.Code == http.StatusNoContent {
		t.Errorf("traversal must NOT succeed; got 204")
	}
	if _, err := os.Stat(filepath.Join(dir, "hacked")); err == nil {
		t.Errorf("file written outside notes/")
	}
}
```

- [ ] **Step 2: Run failing**

```bash
go test ./internal/api/ -run TestGetNote -run TestPutNote
```

Expected: FAIL (handlers undefined).

- [ ] **Step 3: Implement handlers**

```go
// internal/api/notes_handler.go
package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/notes"
)

// maxNoteBytes caps PUT payloads. Notes are short user scratchpad,
// not log dumps — 64KB is generous.
const maxNoteBytes = 64 * 1024

// GetNoteHandler serves the notes body for the session. 200 + body
// on success, 404 when the file doesn't exist (frontend renders the
// empty-state).
func GetNoteHandler(dataDir string) http.Handler {
	root := notes.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
			writeError(w, http.StatusNotFound, "not_found", "no such note")
			return
		}
		path := notes.Path(dataDir, sid)
		clean := filepath.Clean(path)
		if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
			writeError(w, http.StatusNotFound, "not_found", "no such note")
			return
		}
		body, err := os.ReadFile(clean)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "not_found", "no note file")
				return
			}
			writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(body)
	})
}

// PutNoteHandler writes the request body to the session's notes path
// atomically (tmp + rename), capped at maxNoteBytes. 204 on success.
func PutNoteHandler(dataDir string) http.Handler {
	root := notes.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid session id")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNoteBytes+1))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
			return
		}
		if len(body) > maxNoteBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "note exceeds 64KB cap")
			return
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			writeError(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
			return
		}
		final := notes.Path(dataDir, sid)
		clean := filepath.Clean(final)
		if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid session id")
			return
		}
		tmp := clean + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		if err := os.Rename(tmp, clean); err != nil {
			_ = os.Remove(tmp)
			writeError(w, http.StatusInternalServerError, "rename_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
```

- [ ] **Step 4: Run + check**

```bash
go test ./internal/api/ -run TestGetNote -run TestPutNote
```

Expected: PASS all 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/api/notes_handler.go internal/api/notes_handler_test.go
git commit -m "feat(api): GET/PUT /api/sessions/{sid}/note"
```

---

### Task C3: Wire routes + `note_updated` WS frame + watcher boot

**Files:**
- Modify: `internal/api/wsproto.go`
- Modify: `internal/api/router.go`
- Modify: `internal/api/ws.go`
- Modify: `cmd/alfred-server/main.go`

- [ ] **Step 1: Add WS constant**

In `internal/api/wsproto.go`, find the existing summary const:

```go
const TypeSummaryUpdated = "summary_updated"
```

Add immediately after:

```go
const TypeNoteUpdated = "note_updated"
```

The frame reuses the existing `SessionID` field on `OutMsg`; no new field needed.

- [ ] **Step 2: Wire routes**

In `internal/api/router.go`, find the existing summary route (around line 64):

```go
r.Get("/api/sessions/{sid}/summary", GetSummaryHandler(d.Manager.DataDir()).ServeHTTP)
```

Insert immediately after:

```go
r.Get("/api/sessions/{sid}/note", GetNoteHandler(d.Manager.DataDir()).ServeHTTP)
r.Put("/api/sessions/{sid}/note", PutNoteHandler(d.Manager.DataDir()).ServeHTTP)
```

- [ ] **Step 3: Per-WS note watcher subscription**

In `internal/api/ws.go`, find the existing per-WS summary watcher start (search for `summary.StartWatcher`). It looks like:

```go
summaryUpdates := make(chan string, 16)
summaryWatcher, err := summary.StartWatcher(m.DataDir(), func(sid string) {
    select {
    case summaryUpdates <- sid:
    default:
        // dropped — UI will refresh on next manual touch
    }
})
```

Add a sibling block immediately after the summary watcher block:

```go
noteUpdates := make(chan string, 16)
noteWatcher, noteErr := notes.StartWatcher(m.DataDir(), func(sid string) {
    select {
    case noteUpdates <- sid:
    default:
    }
})
if noteErr != nil {
    slog.Warn("notes watcher startup failed; notes UI will be stale", "err", noteErr)
} else {
    defer noteWatcher.Stop()
}
```

Add the import at the top:

```go
"github.com/jesseliu/headless-alfred/internal/notes"
```

In the main `select { ... }` loop in `runClientLoop`, find the summary case:

```go
case sid, ok := <-summaryUpdates:
    if !ok { continue }
    if _, err := m.Get(sid); err != nil { continue }
    _ = write(OutMsg{Type: TypeSummaryUpdated, SessionID: sid})
```

Insert a sibling case right after:

```go
case sid, ok := <-noteUpdates:
    if !ok { continue }
    if _, err := m.Get(sid); err != nil { continue }
    _ = write(OutMsg{Type: TypeNoteUpdated, SessionID: sid})
```

Note: this follows the per-WS-subscription pattern the summary watcher uses (the watcher starts inside the WS handler, one per connection). It's not the most efficient — 8 concurrent WS clients = 8 watchers — but it matches what summary already does and is fine for the use case.

- [ ] **Step 4: Boot — verify summary stays unchanged**

`cmd/alfred-server/main.go` doesn't need to change for notes — the watcher lives per-WS. Confirm `go build ./...` is clean.

```bash
go build ./...
go test ./internal/...
```

Expected: clean + all green.

- [ ] **Step 5: Commit**

```bash
git add internal/api/wsproto.go internal/api/router.go internal/api/ws.go
git commit -m "feat(api): wire note routes + note_updated WS push"
```

---

### Task C4: Frontend — ws + api helpers + reducer counter

**Files:**
- Modify: `web/src/lib/ws.ts`
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/features/sessions/types.ts`
- Modify: `web/src/features/sessions/sessionsReducer.ts`

- [ ] **Step 1: Add `note_updated` to ServerMsg**

In `web/src/lib/ws.ts`, find the existing `summary_updated` variant:

```ts
| { type: 'summary_updated'; sessionID: string }
```

Add immediately after:

```ts
| { type: 'note_updated'; sessionID: string }
```

- [ ] **Step 2: Add API helpers**

In `web/src/lib/api.ts`, append:

```ts
// getNote fetches the notes body for the session. Returns '' for
// 404 (file never created) — the empty state in the UI.
export async function getNote(sessionID: string): Promise<string> {
  try {
    const res = await request(`/api/sessions/${encodeURIComponent(sessionID)}/note`)
    return await res.text()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return ''
    throw e
  }
}

// putNote writes (atomically server-side) the body to the session's
// notes file. Capped at 64KB by the server.
export async function putNote(sessionID: string, body: string): Promise<void> {
  await request(`/api/sessions/${encodeURIComponent(sessionID)}/note`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown' },
    body,
  })
}
```

- [ ] **Step 3: Per-session note counter**

In `web/src/features/sessions/types.ts`, find the `PerSessionState` interface. Add a `noteFetchCounter` field after `summaryFetchCounter`:

```ts
// Bumped on every WS note_updated frame for this session. Mirrors
// summaryFetchCounter; the NotesPanel's read effect depends on it so
// any push triggers a re-fetch.
noteFetchCounter?: number
```

- [ ] **Step 4: Reducer case**

In `web/src/features/sessions/sessionsReducer.ts`, find the `summary_updated` case:

```ts
case 'summary_updated': {
  const cur = prev.get(m.sessionID)
  if (!cur) return { perSession: prev }
  const next = new Map(prev)
  next.set(m.sessionID, {
    ...cur,
    summaryFetchCounter: (cur.summaryFetchCounter ?? 0) + 1,
  })
  return { perSession: next }
}
```

Insert immediately after:

```ts
case 'note_updated': {
  const cur = prev.get(m.sessionID)
  if (!cur) return { perSession: prev }
  const next = new Map(prev)
  next.set(m.sessionID, {
    ...cur,
    noteFetchCounter: (cur.noteFetchCounter ?? 0) + 1,
  })
  return { perSession: next }
}
```

- [ ] **Step 5: TS + tests**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && npx tsc --noEmit && npm test -- --run 2>&1 | tail -4
```

Expected: clean + green.

- [ ] **Step 6: Commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
git add web/src/lib/ws.ts web/src/lib/api.ts web/src/features/sessions/types.ts web/src/features/sessions/sessionsReducer.ts && \
git commit -m "feat(notes): WS variant + getNote/putNote helpers + reducer counter"
```

---

### Task C5: `NotesPanel` component

**Files:**
- Create: `web/src/features/sessions/NotesPanel.tsx`
- Create: `web/src/features/sessions/NotesPanel.css`

An editable textarea. Saves to backend on a 600ms debounced trailing edit (avoid PUT-storm on every keystroke). Refetches on WS push (`noteFetchCounter` bump) — BUT only if the panel isn't currently focused (so the user's in-flight typing doesn't get clobbered by a stale fetch).

- [ ] **Step 1: Write the component**

```tsx
// web/src/features/sessions/NotesPanel.tsx
import { useCallback, useEffect, useRef, useState } from 'react'
import { getNote, putNote } from '../../lib/api'
import './NotesPanel.css'

interface Props {
  sessionID: string
  // Bumps when the backend reports a note_updated frame. The panel
  // refetches IF the textarea isn't currently focused (so a server
  // echo doesn't overwrite the user's mid-typing buffer).
  noteFetchCounter: number
}

const SAVE_DEBOUNCE_MS = 600

export function NotesPanel({ sessionID, noteFetchCounter }: Props) {
  const [text, setText] = useState<string>('')
  const [loaded, setLoaded] = useState(false)
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)

  // Snapshot the "last server-side value" so we can avoid re-PUTting
  // when text == server (the read effect just hydrated us).
  const lastPushedRef = useRef<string>('')

  // Initial fetch + WS-driven refetch.
  //
  // Deps deliberately exclude `loaded` — including it would trigger a
  // second fetch the moment setLoaded(true) runs. The first fetch is
  // unconditional (loaded starts as false; we WANT it to override the
  // empty textarea). Subsequent fetches are gated by the focus check
  // so a server echo doesn't clobber the user's in-flight typing.
  useEffect(() => {
    let alive = true
    const isFirstFetch = !loaded
    getNote(sessionID)
      .then((body) => {
        if (!alive) return
        if (!isFirstFetch && document.activeElement === taRef.current) {
          // User is typing right now — skip the refetch. The local
          // text is more current; the next debounced PUT will catch
          // server up.
          return
        }
        setText(body)
        lastPushedRef.current = body
        setError(null)
      })
      .catch((e) => {
        if (!alive) return
        setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        if (!loaded) setLoaded(true)
      })
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionID, noteFetchCounter])

  // Debounced save.
  useEffect(() => {
    if (!loaded) return
    if (text === lastPushedRef.current) return
    const handle = setTimeout(() => {
      putNote(sessionID, text)
        .then(() => {
          lastPushedRef.current = text
          setSavedAt(Date.now())
          setError(null)
        })
        .catch((e) => {
          setError(e instanceof Error ? e.message : String(e))
        })
    }, SAVE_DEBOUNCE_MS)
    return () => clearTimeout(handle)
  }, [text, sessionID, loaded])

  const onChange = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setText(e.target.value)
  }, [])

  return (
    <div className="notes-panel">
      <textarea
        ref={taRef}
        className="notes-panel__textarea"
        value={text}
        onChange={onChange}
        placeholder="Personal notes for this session. Markdown ok. Not sent to Claude."
        spellCheck={false}
      />
      <div className="notes-panel__footer">
        {error && <span className="notes-panel__error">{error}</span>}
        {!error && savedAt && (
          <span className="notes-panel__saved">Saved · {new Date(savedAt).toLocaleTimeString()}</span>
        )}
        {!error && !savedAt && loaded && (
          <span className="notes-panel__hint">Autosaves while you type</span>
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Styles**

```css
/* web/src/features/sessions/NotesPanel.css */
.notes-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.notes-panel__textarea {
  flex: 1;
  min-height: 120px;
  resize: none;
  padding: 10px 12px;
  background: rgba(0, 0, 0, 0.18);
  border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 6px;
  color: var(--text, #eaeaea);
  font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  line-height: 1.55;
  outline: none;
}

.notes-panel__textarea:focus {
  border-color: rgba(122, 162, 247, 0.4);
}

.notes-panel__footer {
  padding: 6px 2px 0;
  font-size: 11px;
  color: var(--text-muted, #9aa3b2);
  min-height: 14px;
}

.notes-panel__saved {
  color: #7ed492;
}

.notes-panel__error {
  color: #e88c8c;
}
```

- [ ] **Step 3: TS + tests**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && npx tsc --noEmit && npm test -- --run 2>&1 | tail -4
```

Expected: clean + green. No new unit tests at this stage — the component is exercised end-to-end by the manual smoke + the eventual workspace tests.

- [ ] **Step 4: Commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
git add web/src/features/sessions/NotesPanel.tsx web/src/features/sessions/NotesPanel.css && \
git commit -m "feat(notes): NotesPanel component with debounced autosave"
```

---

### Task C6: `RightRail` accordion container

**Files:**
- Create: `web/src/features/sessions/RightRail.tsx`
- Create: `web/src/features/sessions/RightRail.css`
- Modify: `web/src/features/sessions/SummarySidebar.tsx`

`RightRail` is the right-column container. It renders Summary (if Claude UI mode + template) AND Notes (always, in any session) as accordion sections. SummarySidebar's outer `<aside>` and header are pulled OUT into RightRail; SummarySidebar becomes just the content area.

- [ ] **Step 1: Refactor SummarySidebar to drop wrapper**

Read the current `SummarySidebar.tsx`. The `<aside className="summary-sidebar">…<header>…</header><div className="summary-sidebar__body">{content}</div></aside>` structure needs to become just the `{content}` part. Rename the file's exported function from `SummarySidebar` to `SummarySection` for clarity.

Replace `web/src/features/sessions/SummarySidebar.tsx` body's return statement:

```tsx
  return (
    <SummaryView
      text={summary}
      loading={summaryLoading}
      error={summaryErr}
    />
  )
```

…and rename the exported function:

```tsx
export function SummarySection({ sessionID, summaryFetchCounter }: Props) {
```

Delete the `<aside>` wrapper, the `<header>`, and the close button (already removed in an earlier feature). Keep `SummaryView` helper as-is.

- [ ] **Step 2: Trim `SummarySidebar.css`**

Open `web/src/features/sessions/SummarySidebar.css`. Delete every rule that targets `.summary-sidebar` or `.summary-sidebar__*` EXCEPT the ones describing the inner content (`__placeholder`, `__error`, `__markdown`). The wrapper and header rules are moving to RightRail.

- [ ] **Step 3: Write RightRail.tsx**

```tsx
// web/src/features/sessions/RightRail.tsx
import { useCallback, useEffect, useState } from 'react'
import { SummarySection } from './SummarySidebar'
import { NotesPanel } from './NotesPanel'
import './RightRail.css'

interface Props {
  sessionID: string
  showSummary: boolean              // claude+ui+summary-todo
  summaryFetchCounter: number
  noteFetchCounter: number
}

// localStorage flags for each section's collapsed state. Both default
// to expanded the first time the user sees them.
const LS_SUMMARY = 'alfred_right_rail_summary_collapsed'
const LS_NOTES   = 'alfred_right_rail_notes_collapsed'

function readBool(key: string): boolean {
  try { return localStorage.getItem(key) === '1' } catch { return false }
}

function writeBool(key: string, v: boolean): void {
  try { localStorage.setItem(key, v ? '1' : '0') } catch { /* ignore */ }
}

export function RightRail({ sessionID, showSummary, summaryFetchCounter, noteFetchCounter }: Props) {
  const [summaryCollapsed, setSummaryCollapsed] = useState<boolean>(() => readBool(LS_SUMMARY))
  const [notesCollapsed, setNotesCollapsed] = useState<boolean>(() => readBool(LS_NOTES))

  // Whenever the eligible-sections set changes, ensure at least one
  // is expanded so the rail isn't a useless wall of headers.
  useEffect(() => {
    if (!showSummary && notesCollapsed) {
      setNotesCollapsed(false)
      writeBool(LS_NOTES, false)
    }
  }, [showSummary, notesCollapsed])

  const toggleSummary = useCallback(() => {
    setSummaryCollapsed((v) => { writeBool(LS_SUMMARY, !v); return !v })
  }, [])
  const toggleNotes = useCallback(() => {
    setNotesCollapsed((v) => { writeBool(LS_NOTES, !v); return !v })
  }, [])

  return (
    <aside className="right-rail" aria-label="Right rail">
      {showSummary && (
        <section className={`right-rail__section ${summaryCollapsed ? 'is-collapsed' : ''}`}>
          <button
            type="button"
            className="right-rail__header"
            onClick={toggleSummary}
            aria-expanded={!summaryCollapsed}
          >
            <span className="right-rail__chev">{summaryCollapsed ? '▸' : '▾'}</span>
            <h2 className="right-rail__title">Task Summary</h2>
          </button>
          {!summaryCollapsed && (
            <div className="right-rail__body">
              <SummarySection sessionID={sessionID} summaryFetchCounter={summaryFetchCounter} />
            </div>
          )}
        </section>
      )}
      <section className={`right-rail__section ${notesCollapsed ? 'is-collapsed' : ''}`}>
        <button
          type="button"
          className="right-rail__header"
          onClick={toggleNotes}
          aria-expanded={!notesCollapsed}
        >
          <span className="right-rail__chev">{notesCollapsed ? '▸' : '▾'}</span>
          <h2 className="right-rail__title">Notes</h2>
          <span className="right-rail__title-hint">(local only)</span>
        </button>
        {!notesCollapsed && (
          <div className="right-rail__body right-rail__body--notes">
            {/* key={sessionID} forces a fresh NotesPanel mount per
                session. Without it, the panel's lastPushedRef and
                text state would carry over when the user switches
                sessions, and the trailing debounced PUT would write
                the OLD text to the NEW session's file. */}
            <NotesPanel key={sessionID} sessionID={sessionID} noteFetchCounter={noteFetchCounter} />
          </div>
        )}
      </section>
    </aside>
  )
}
```

- [ ] **Step 4: Write RightRail.css**

```css
/* web/src/features/sessions/RightRail.css */
.right-rail {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-elevated, #1a1d24);
  border-left: 1px solid rgba(255, 255, 255, 0.06);
  color: var(--text, #eaeaea);
  overflow: hidden;
}

.right-rail__section {
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}

.right-rail__section:last-child {
  border-bottom: none;
  flex: 1;
}

.right-rail__section.is-collapsed {
  flex: 0 0 auto;
}

.right-rail__header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  background: transparent;
  border: none;
  color: var(--text, #eaeaea);
  text-align: left;
  cursor: pointer;
}

.right-rail__header:hover {
  background: rgba(255, 255, 255, 0.04);
}

.right-rail__chev {
  color: var(--text-muted, #9aa3b2);
  font-size: 10px;
  width: 10px;
}

.right-rail__title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}

.right-rail__title-hint {
  margin-left: auto;
  font-size: 10px;
  color: var(--text-muted, #9aa3b2);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.right-rail__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 8px 14px 14px;
}

.right-rail__body--notes {
  padding: 8px 14px 14px;
  display: flex;
}
```

- [ ] **Step 5: TS + tests + Recap sanity**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && npx tsc --noEmit && npm test -- --run 2>&1 | tail -4
```

Expected: clean + green. SummarySidebar's previous test (if any references it) — check whether anything imports `SummarySidebar` by old name. If so, those tests need the import renamed to `SummarySection`.

ALSO check that the refactor didn't break the Recap path. Open the rendered RecapSidebar (still mounted unchanged) and confirm:

```bash
grep -n "MarkdownView" web/src/features/sessions/RecapSidebar.tsx
grep -n "SummarySidebar\|SummarySection" web/src/features/sessions/RecapSidebar.tsx
```

Expected: RecapSidebar still imports `MarkdownView` (shared component); it does NOT import `SummarySidebar` / `SummarySection`. If by accident it does, fix that import — RecapSidebar should remain entirely independent.

- [ ] **Step 6: Commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
git add web/src/features/sessions/RightRail.tsx web/src/features/sessions/RightRail.css \
        web/src/features/sessions/SummarySidebar.tsx web/src/features/sessions/SummarySidebar.css && \
git commit -m "feat(rail): RightRail accordion; refactor SummarySidebar -> SummarySection"
```

---

### Task C7: Mount RightRail in WorkspacePage

**Files:**
- Modify: `web/src/features/sessions/WorkspacePage.tsx`

The current right pane mount is `<SummarySidebar … />` gated on `showSummarySidebar`. Replace with `<RightRail …/>` gated on a broader predicate: any session in Claude UI OR shell mode that ISN'T a recap.

- [ ] **Step 1: Update imports + computed predicates**

Open `web/src/features/sessions/WorkspacePage.tsx`. Replace the `SummarySidebar` import:

```tsx
import { SummarySidebar } from './SummarySidebar'
```

With:

```tsx
import { RightRail } from './RightRail'
```

Find the existing `showSummarySidebar` / `showRecapSidebar` computations (around line 88). Replace:

```tsx
const showSummarySidebar = !!(selected && ps && ps.mode === 'claude' && ps.templateId === 'summary-todo' && !isRecap)
const showRecapSidebar = !!(selected && isRecap)
const sidebarShown = (showSummarySidebar && !sidebarHidden) || showRecapSidebar
```

With:

```tsx
// RightRail (Summary + Notes) is eligible for any non-recap session.
// Notes always renders; Summary nested inside only when claude+ui+template.
const showRightRail = !!(selected && ps && !isRecap)
const showSummarySection = !!(selected && ps && ps.mode === 'claude' && ps.templateId === 'summary-todo' && !isRecap)
const showRecapSidebar = !!(selected && isRecap)
const sidebarShown = (showRightRail && !sidebarHidden) || showRecapSidebar
```

- [ ] **Step 2: Replace the SummarySidebar mount**

Find the existing block:

```tsx
{showSummarySidebar && !sidebarHidden && selected && ps && (
  <SummarySidebar
    key={selected.id}
    sessionID={selected.id}
    summaryFetchCounter={ps.summaryFetchCounter ?? 0}
  />
)}
```

Replace with:

```tsx
{showRightRail && !sidebarHidden && selected && ps && (
  <RightRail
    key={selected.id}
    sessionID={selected.id}
    showSummary={showSummarySection}
    summaryFetchCounter={ps.summaryFetchCounter ?? 0}
    noteFetchCounter={ps.noteFetchCounter ?? 0}
  />
)}
```

- [ ] **Step 3: TS + tests**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && npx tsc --noEmit && npm test -- --run 2>&1 | tail -4
```

Expected: clean + green.

- [ ] **Step 4: Commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
git add web/src/features/sessions/WorkspacePage.tsx && \
git commit -m "feat(workspace): mount RightRail (Summary+Notes accordion) for non-recap sessions"
```

---

### Task C8: Manual end-to-end verification

This is a verification task, not a code change. Captures evidence.

- [ ] **Step 1: Rebuild + restart**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
pkill -f /tmp/alfred-server 2>/dev/null; sleep 1 && \
go build -o /tmp/alfred-server ./cmd/alfred-server && \
ALFRED_DATA_DIR=/tmp/alfred-dev/data ALFRED_ADDR=:8080 \
ALFRED_USER=admin ALFRED_PASSWORD=admin ALFRED_TOKEN=devtoken \
nohup /tmp/alfred-server > /tmp/alfred-server.log 2>&1 & \
sleep 2 && \
curl -s -o /dev/null -w "backend %{http_code}\n" http://localhost:8080/api/health
```

Expected: `backend 200`.

- [ ] **Step 2: Drive the UI**

Write a verify script `web/verify-right-rail.mjs`:

```js
import { chromium } from 'playwright'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
const __dirname = path.dirname(fileURLToPath(import.meta.url))
const SHOTS = path.join(__dirname, '.screenshots', 'right-rail')
fs.mkdirSync(SHOTS, { recursive: true })
const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:5173'
const TOK = 'devtoken'

const browser = await chromium.launch({ headless: true })
const page = await (await browser.newContext({ viewport: { width: 1400, height: 900 } })).newPage()
await page.goto(FRONTEND)
await page.evaluate(t => { localStorage.setItem('alfred_token', t); localStorage.removeItem('alfred_selected_session') }, TOK)
await page.reload()
await page.waitForSelector('text=+ New chat', { timeout: 10000 })

// SCENARIO A: shell session — Notes-only.
const { id: sidA } = await fetch(`${BACKEND}/api/sessions`, {
  method: 'POST', headers: { Authorization: `Bearer ${TOK}`, 'Content-Type': 'application/json' },
  body: JSON.stringify({ name: 'rr-shell' }),
}).then(r => r.json())
await page.evaluate(s => localStorage.setItem('alfred_selected_session', s), sidA)
await page.reload()
await page.waitForSelector('.workspace__claude-btn', { timeout: 10000 })
const railA = await page.locator('.right-rail').isVisible().catch(() => false)
const summaryHeaderA = await page.locator('.right-rail__title:has-text("Task Summary")').count()
const notesHeaderA = await page.locator('.right-rail__title:has-text("Notes")').count()
console.log('Shell session — right rail visible:', railA)
console.log('  Summary header count (want 0):', summaryHeaderA)
console.log('  Notes header count (want 1):', notesHeaderA)
await page.screenshot({ path: path.join(SHOTS, '01-shell-notes-only.png'), fullPage: true })

// Type a note, wait for save
await page.locator('.notes-panel__textarea').fill('test-note-1')
await page.waitForTimeout(900) // > 600ms debounce
const noteServer = await fetch(`${BACKEND}/api/sessions/${sidA}/note`, {
  headers: { Authorization: `Bearer ${TOK}` },
}).then(r => r.text())
console.log('  server has note after typing:', noteServer === 'test-note-1')

// SCENARIO B: Claude UI + summary — both sections
await page.locator('.workspace__claude-btn').click()
await page.locator('label:has-text("Chat UI")').click()
await page.locator('button:has-text("Start")').click()
await page.waitForSelector('.right-rail', { timeout: 10000 })
const summaryHeaderB = await page.locator('.right-rail__title:has-text("Task Summary")').count()
const notesHeaderB = await page.locator('.right-rail__title:has-text("Notes")').count()
console.log('Claude UI — Summary count:', summaryHeaderB, 'Notes count:', notesHeaderB)
await page.screenshot({ path: path.join(SHOTS, '02-claude-both.png'), fullPage: true })

// Toggle Summary section collapse
await page.locator('.right-rail__header:has-text("Task Summary")').click()
await page.waitForTimeout(200)
await page.screenshot({ path: path.join(SHOTS, '03-summary-collapsed.png'), fullPage: true })

// Toggle sidebar via header icon
await page.locator('.workspace__sidebar-icon-btn').click()
await page.waitForTimeout(200)
const railHidden = !(await page.locator('.right-rail').isVisible().catch(() => false))
console.log('Sidebar icon hides rail:', railHidden)
await page.screenshot({ path: path.join(SHOTS, '04-rail-hidden.png'), fullPage: true })

await fetch(`${BACKEND}/api/sessions/${sidA}`, { method: 'DELETE', headers: { Authorization: `Bearer ${TOK}` } })
await browser.close()
console.log('screenshots:', SHOTS)
```

Run:

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && node verify-right-rail.mjs
```

Expected output:
- `Shell session — right rail visible: true`
- `Summary header count (want 0): 0`
- `Notes header count (want 1): 1`
- `server has note after typing: true`
- `Claude UI — Summary count: 1 Notes count: 1`
- `Sidebar icon hides rail: true`

- [ ] **Step 3: Cleanup verify script + commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && rm web/verify-right-rail.mjs
# No source code change beyond what's already committed; this task is verification only.
```

If any assertion failed, debug at the implementation step that introduced the gap — not here.

---

### Task C9: Update CONTEXT.md

**Files:**
- Modify: `CONTEXT.md`

Add brief notes to (a) the "Quick orientation" table and (b) the "Non-obvious traps" table.

- [ ] **Step 1: Add quick-orientation row**

Find the existing row:

```
| Per-session summary template | … |
```

Add immediately after:

```
| Per-session Notes (user-only scratchpad, not sent to Claude) | Backend mirrors `internal/summary`: `internal/notes/{path,watcher}.go` + `internal/api/notes_handler.go` (GET + PUT, 64KB cap, atomic tmp+rename). `note_updated` WS frame pushed per-WS-subscription same as summary. Frontend: `NotesPanel.tsx` with 600ms debounced autosave + focus-guarded refetch (in-flight typing is never clobbered by a server echo). Mounted in `RightRail.tsx` accordion alongside Summary. Notes are deliberately NOT passed through `composePromptText` — Claude never sees them. |
```

- [ ] **Step 2: Add traps row**

Find a good insertion point in the traps table (after the recap-related rows). Add:

```
| The right rail is now a RightRail accordion (Summary + Notes), not a SummarySidebar. The header `Summary` text button became a `workspace__sidebar-icon-btn` SVG icon that toggles the whole rail. If you re-introduce a "summary-only" predicate (e.g. ` && ps.templateId === 'summary-todo'`) on the WHOLE rail you'll break Notes for sessions without the summary template | Notes panel vanishes for shell sessions or claude sessions without summary opt-in, breaking the feature | `showRightRail = mode is set && !isRecap` is the WHOLE-rail predicate; `showSummarySection` (passed to RightRail as a prop) is the inner-section gate. Toggling the icon only flips `sidebarHidden`; accordion sections inside have their own localStorage keys (`alfred_right_rail_{summary,notes}_collapsed`) |
```

- [ ] **Step 3: Commit**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred && \
git add CONTEXT.md && \
git commit -m "docs(CONTEXT): RightRail accordion + Notes panel; sidebar icon predicate"
```

---

## Final verification (all three features)

- [ ] **Backend tests**

```bash
go test ./internal/...
```

Expected: all PASS, including new `internal/notes` and the notes_handler tests.

- [ ] **Race check**

```bash
go test -race ./internal/notes/... ./internal/api/...
```

Expected: clean.

- [ ] **Frontend tests**

```bash
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred/web && npx tsc --noEmit && npm test -- --run
```

Expected: clean + all green.

- [ ] **End-to-end manual checks**

1. **Feature A** — Claude UI session, send a prompt that fires Read+Bash. Each tool row is a single line with `▸` chevron. Click expands.
2. **Feature B** — Header shows a sidebar icon (no text "Summary"). Clicking toggles the whole right rail.
3. **Feature C** — In a shell session, the rail shows only "Notes" (no "Task Summary"). Type — wait — refresh page — note persists. In a Claude UI session with summary opted in, rail shows both Summary and Notes; each section's chevron toggles independently.
