# Per-session prompt templates with task-list summary as v1's only built-in

**Goal:** Give the user a way to keep Claude's context fresh across turns
without re-explaining themselves every prompt. v1 ships one built-in
template ("task-list summary") that appends a small instruction to every
user prompt and asks Claude to maintain a short on-disk summary file.
The summary is rendered in a new right-hand sidebar in the Claude UI
mode chat view. Templates are read-only in the UI — the user adjusts
content by talking to Claude.

**Why now:** The Claude UI mode is on `main` and is the only mode where
this makes sense (TUI users already have full claude TUI features like
`/memory`, `/compact`). Without this, every Claude UI conversation
slowly forgets what it was doing as the transcript scrolls past the
prompt cache window — and the user has to re-declare working dir,
conventions, current task on every prompt.

**Out of scope for v1:**

- Multiple built-in templates (only `summary-todo`).
- User-authored custom templates (templates are server-side constants
  in `internal/template/builtin.go`; adding more is a code change).
- Editable templates from the UI (read-only viewer only).
- TUI mode (TUI uses claude's own session machinery).
- Cross-session summary aggregation.

## User experience

### Enabling

The Start Claude dialog (Chat UI renderer) gains a checkbox below the
existing bypass checkbox:

```
☑ Maintain a task summary  (Recommended)
   After every reply, Claude updates a small task summary
   you can read in the right sidebar. Lets you pick up where
   you left off without re-explaining yourself.
```

Default ON when Chat UI is picked. Unchecking → no template. Choice
is captured by the existing `enter_claude` WS frame via a new
`templateId?: string` field.

### Reading the summary

A new third column appears in the workspace (left: session list, middle:
chat, right: summary), always visible in Chat UI mode. Width ~280 px.
It shows the current session's summary file rendered with
`react-markdown` + `remark-gfm` (same stack as assistant text).

Three states:

- **Template not enabled for this session:** "Summary tracking is off
  for this session. Start a new Chat UI session and check the box to
  enable it."
- **Template enabled but file empty / missing:** "No summary yet —
  send your first prompt and Claude will populate this."
- **File present:** rendered markdown.

A small chevron at the top of the sidebar toggles to a "Template" view
showing the read-only template content the user opted in to. Closing
returns to summary view.

A close button (×) at the top hides the sidebar; a thin re-open tab on
the right edge re-shows it. Hidden state is **global**, stored in
`localStorage['alfred_summary_sidebar_hidden']`. Once the user clicks
× the sidebar stays hidden across session switches, page reloads,
and even sessions that don't have a template enabled — the user has
to click the re-open tab to bring it back. Rationale: switching
sessions shouldn't undo a deliberate "hide" gesture. The re-open tab
is always present in Chat UI mode regardless of whether the
currently-selected session has a template.

### Updating

The user does NOT edit the summary directly. Claude updates it via the
Write tool every turn, prompted by the appended template text. If the
user disagrees with what's in the summary, they tell Claude in the next
prompt ("the goal is X, not Y; please fix the summary"). Claude rewrites
on the next reply.

## Architecture

```
┌── frontend ──────────────────────────────────────────────────────┐
│                                                                  │
│  StartClaudeDialog                                               │
│    └── checkbox → onStart(renderer, bypass, templateId?)         │
│         └── enterClaude(sid, renderer, bypass, templateId)       │
│              └── WS: enter_claude { templateId }                 │
│                                                                  │
│  WorkspacePage                                                   │
│    └── 3-column grid                                             │
│         ├── SessionsSidebar (existing)                           │
│         ├── ClaudeChatView   (existing)                          │
│         └── SummarySidebar   (new)                               │
│              ├── on selectedSessionID change → GET summary       │
│              ├── on WS summary_updated → GET summary again       │
│              └── on "view template" → GET template body          │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
                            │
                            ▼ HTTP / WS
┌── alfred-server ─────────────────────────────────────────────────┐
│                                                                  │
│  /api/sessions/{sid}/summary    GET  →  file body (md, may 404)  │
│  /api/templates/{templateId}    GET  →  template content         │
│                                                                  │
│  WS frame  { type: 'summary_updated', sessionID }                │
│                                                                  │
│  handleClaudePrompt                                              │
│    ├── m.GetTemplateID(sid) → string                             │
│    └── if non-empty: prompt = userText + "\n\n---\n" +           │
│                       template.Render(tplID, sid)                │
│                                                                  │
│  internal/template/                                              │
│    ├── builtin.go     Templates registry (one entry: summary-todo)│
│    └── render.go      Render(id, sid) substitutes <sid> + path   │
│                                                                  │
│  internal/summary/                                               │
│    ├── path.go        Path(sid) → ALFRED_DATA_DIR/summaries/<sid>.md│
│    └── watcher.go     fsnotify watcher → pushes summary_updated  │
│                                                                  │
│  internal/claude/dispatcher.go                                   │
│    └── auto-allow Write tool when path matches summary path      │
│        for the originating session                               │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
                            │
                            ▼ fsnotify
        ALFRED_DATA_DIR/summaries/<sessionID>.md  (one file per session)
```

## Components

### 1. SessionMeta extension

`internal/store/sessions.go`:

```go
type SessionMeta struct {
    // ... existing fields ...

    // TemplateID names the active prompt template, e.g. "summary-todo".
    // Empty = no template (no per-turn injection, no summary file
    // maintained). Set on the first enter_claude with renderer=ui;
    // reset by Reconcile on Pod restart along with Renderer.
    TemplateID string `json:"template_id,omitempty"`
}
```

`Manager.GetTemplateID(sid)`, `SetTemplateID(sid, id)`, mirror existing
`Renderer` getters. Reconcile clears it alongside Renderer.

### 2. Templates registry — `internal/template/`

`builtin.go`:

```go
type Template struct {
    ID      string
    Name    string
    Content string  // raw with <sid> and <summary_path> placeholders
}

var Builtins = map[string]Template{
    "summary-todo": {
        ID:      "summary-todo",
        Name:    "Task summary",
        Content: `After your reply, update the session summary at
<summary_path> so we don't lose context across turns.

Steps:

1. Use Read on <summary_path> first if it exists; preserve
   still-relevant content, remove obsolete items.
2. Rewrite the whole file in this shape (keep the WHOLE file under
   1500 characters — short bullets, no narrative paragraphs, no
   verbatim code blocks):

## Goal
<one line: what we're trying to achieve>

## Status
<one line: in progress / blocked on X / done>

## Decisions
- <terse bullets of what we've agreed on>

## Open questions
- <things still unresolved>

3. Use Write (one tool call, full file contents).
`,
    },
}
```

`render.go`:

```go
func Render(id, sid, summaryPath string) string {
    t, ok := Builtins[id]
    if !ok { return "" }
    s := t.Content
    s = strings.ReplaceAll(s, "<sid>", sid)
    s = strings.ReplaceAll(s, "<summary_path>", summaryPath)
    return s
}
```

Renders to plain text; no other format magic. Future templates use
the same placeholder convention.

### 3. Summary file path — `internal/summary/path.go`

```go
// Path returns the on-disk summary path for the session.
// ALFRED_DATA_DIR/summaries/<sid>.md. Caller is responsible for
// ensuring the parent directory exists (the watcher creates it
// on startup).
func Path(dataDir, sid string) string {
    return filepath.Join(dataDir, "summaries", sid+".md")
}
```

Wired through `Manager.cfg.DataDir` so handlers can compute it without
a global.

### 4. Prompt injection — `handleClaudePrompt`

```go
text := msg.Text
if tplID := m.GetTemplateID(msg.SessionID); tplID != "" {
    summaryPath := summary.Path(m.DataDir(), msg.SessionID)
    if rendered := tpl.Render(tplID, msg.SessionID, summaryPath); rendered != "" {
        text = text + "\n\n---\n" + rendered
    }
}
// rest of existing flow uses `text`
```

Empty/unknown templateId silently skips injection — keeps backward
compatibility with old sessions that don't have one.

### 5. Dispatcher auto-allow — `internal/claude/dispatcher.go`

Today: dispatcher routes every PreToolUse approval to the WS
subscriber. We need a fourth fallback before that: if the tool is
`Read` or `Write` AND the input's path is **exactly** the summary
path for the matched Alfred session AND (for Write) the content
size is reasonable → auto-allow without bothering the user.

```go
// inside OnAsk, after lookup() succeeds:
if isSummaryIO(req, alfredSID, m.DataDir()) {
    autoAllow(req.ToolUseID)
    return
}
// ... existing channel push path
```

`isSummaryIO`:

```go
const maxSummaryWriteBytes = 8 * 1024 // 8 KB; the template asks for
                                       // "short bullets, no narrative"
                                       // — anything larger is wrong.

func isSummaryIO(req PendingRequest, alfredSID, dataDir string) bool {
    wantPath := summary.Path(dataDir, alfredSID)
    switch req.ToolName {
    case "Read":
        var in struct { FilePath string `json:"file_path"` }
        if json.Unmarshal(req.ToolInput, &in) != nil { return false }
        return in.FilePath == wantPath
    case "Write":
        var in struct {
            FilePath string `json:"file_path"`
            Content  string `json:"content"`
        }
        if json.Unmarshal(req.ToolInput, &in) != nil { return false }
        if in.FilePath != wantPath { return false }
        if len(in.Content) > maxSummaryWriteBytes { return false }
        return true
    }
    return false
}
```

Reasoning:

- Read AND Write both need auto-allow — the template instructs Claude
  to Read first then Write. Auto-allowing only Write would still pop
  an approval card every turn for the Read.
- `wantPath` is computed from the dispatcher-side `alfredSID` (which
  we looked up — not user input), so path traversal via `<sid>` is
  not possible.
- The `==` comparison is strict-string-equal (after Go's normal
  syscall canonicalisation when the file is actually opened). No
  prefix matching, no glob.
- Write content cap (`8 KB`) defends against a prompt-injection
  attacker convincing Claude to overwrite the summary with a huge
  blob. Anything legitimately summary-shaped is well under 2 KB.
  Oversized writes fall through to the normal approval flow — the
  user sees them and can deny.
- Other paths, other tools, and oversized Writes still go through
  the normal approval channel — user retains full control.

### 6. Watcher — `internal/summary/watcher.go`

`fsnotify`-based watcher started by `cmd/alfred-server/main.go` after
the data dir is known:

```go
type Watcher struct {
    dir     string
    onWrite func(sessionID string)
}
```

- Watches `<dataDir>/summaries/` (creates the dir on Start if missing).
- For each `Create`/`Write` event, derives sessionID from the filename
  (`<sid>.md` → `<sid>`), invokes `onWrite(sid)`.
- `onWrite` calls into the WS layer to push `summary_updated` to all
  connected subscribers.

The watcher dedups bursts via a small debounce (200 ms per filename),
otherwise saving a 50-byte summary fires several Write events.

### 7. WS protocol additions — `internal/api/wsproto.go`

**Inbound** (`InMsg`): `TemplateID string \`json:"templateId,omitempty"\``
on `enter_claude`.

**Outbound** (`OutMsg`): new frame type `summary_updated`:

```go
{ type: "summary_updated", sessionID: "..." }
```

Carries no body — the frontend re-fetches the file via HTTP. The
backend sends the frame **only on WS connections that are subscribed
to the affected session** (the same per-session subscriber set that
already routes `started` / `chunk` / `done` / `claude_event` to the
right tab). v1 is single-user so this is just defensive hygiene; if
the deploy ever grows to multi-user, summary updates don't leak
across users.

### 8. HTTP endpoints

`GET /api/sessions/{sid}/summary`
- 200 → `text/markdown` body (file content, possibly empty)
- 404 → file doesn't exist (frontend renders empty state)
- 403 → if `sid` is unknown to the manager (defensive)

The frontend treats both 404 AND a 200 with `body.trim() === ''` as
the empty state. Claude has been seen to Write a literally-empty
summary occasionally; we don't want a blank pane just because the
file exists.

`GET /api/templates/{id}`
- 200 → `text/plain` body (raw template content, including `<sid>` and
  `<summary_path>` placeholders so the user sees what gets injected)
- 404 → unknown id

Both routes auth via the existing bearer-token middleware. No write
endpoints — UI is read-only.

### 9. Frontend changes

**StartClaudeDialog** (Chat UI only): new checkbox below bypass:

```tsx
<label className="start-claude__checkbox">
  <input type="checkbox" checked={summary} onChange={…} />
  <div>
    <div className="start-claude__checkbox-title">
      Maintain a task summary
    </div>
    <div className="start-claude__checkbox-desc">
      After every reply, Claude updates a short summary in
      the right sidebar. Lets you pick up where you left off.
    </div>
  </div>
</label>
```

`onStart` signature → `(renderer, bypass, templateId?: string)`.
Checkbox checked → `templateId = "summary-todo"`. Unchecked or TUI
renderer → no `templateId`.

**`useSessions.enterClaude`** → forwards `templateId` to the WS frame.

**`WorkspacePage`** layout:

```css
.workspace {
  display: grid;
  grid-template-columns: <sidebar> 1fr <summary-sidebar>;
}
```

The summary sidebar is only rendered when `ps.renderer === 'ui'`
(matches the chat view condition); for TUI sessions the grid collapses
to two columns.

**`SummarySidebar`** (new component):

- Subscribes to `selectedSessionID` and `perSession[selectedSessionID].templateId`.
- On mount + on `selectedSessionID` change → fetch `/api/sessions/<sid>/summary`.
- WS handler in `useSessions` for `summary_updated` → if frame.sessionID
  matches the currently selected, increments a "fetch counter" → the
  sidebar re-fetches.
- Renders one of three states: disabled, empty, content.
- Top-right chevron: switches between "Summary" and "Template" views.
  Template view fetches `/api/templates/{templateId}` once and caches
  in component state.
- Top-right ×: sets `localStorage['alfred_summary_sidebar_hidden'] = '1'`.
  When hidden, a thin clickable tab on the right edge restores it.

**Optimistic refresh**: when the user sends a `claude_prompt`, the
sidebar visually fades to indicate "next-summary incoming" until the
next `summary_updated` lands. No real loading state needed since
fetch is fast.

### 10. Tests

- **Go unit:**
  - `template.Render` substitutes placeholders correctly; unknown id
    returns "".
  - `summary.Path` produces the expected join.
  - `dispatcher.OnAsk` auto-allows Write to the summary path, denies
    Write to anything else, allows the existing 3 paths.
  - `summary.Watcher` debounces bursts (10 file writes → 1 callback).
- **Vitest:**
  - `SummarySidebar` renders all three states.
  - View switcher toggles between Summary and Template.
  - `localStorage` persistence of hidden state.
  - Reducer: `summary_updated` frame increments the fetch counter.
- **Playwright e2e:**
  - In Chat UI mode, enable summary template → POST a synthetic
    `summary_updated` via a hand-crafted file write (script writes to
    the summary path directly) → assert the sidebar updates within 2s.
  - "View template" reveals the raw template text.
  - Disable checkbox in dialog → sidebar shows the "tracking disabled"
    state.

## Data flow — happy path

```
1. User opens Start Claude dialog, picks Chat UI, leaves both checkboxes
   on (bypass + summary), clicks Start.

2. Frontend sends:
       { type: "enter_claude", sessionID, renderer: "ui",
         bypassPermissions: true, templateId: "summary-todo" }

3. Backend handleEnterClaude:
       - SetMode(claude), SetRenderer("ui"), SetClaudeBypass(true)
       - SetTemplateID(sid, "summary-todo")
       - emits claude_entered

4. Sidebar fetches /api/sessions/<sid>/summary → 404 → renders
   "No summary yet" empty state.

5. User types "build me a fizzbuzz", presses Enter.

6. handleClaudePrompt:
       - reads TemplateID = "summary-todo"
       - rendered = template.Render(id, sid, summaryPath)
       - text = "build me a fizzbuzz\n\n---\n<rendered template>"
       - forks claude -p with the combined text

7. Claude streams text + Tool calls. At end, it Writes to
   /data/summaries/<sid>.md (Write tool).

8. PreToolUse hook fires → bridge → dispatcher.OnAsk:
       - lookup(claude_session_id) = sid (matches)
       - isSummaryWrite(req, sid, dataDir) → true
       - autoAllow → no card shown to user

9. claude completes the Write.

10. fsnotify watcher sees fs.Create + fs.Write events for <sid>.md.
    After debounce, calls onWrite(sid).

11. WS broadcasts { type: "summary_updated", sessionID: sid }.

12. Frontend useSessions handler sees frame, sessionID matches the
    selected session → triggers SummarySidebar re-fetch.

13. SummarySidebar GET /api/sessions/<sid>/summary → 200 with the
    fresh content → react-markdown renders.

Total latency from claude's Write to UI update: ~50-200 ms.
```

## Error handling

- **fsnotify fails on Start** (rare; macOS limit, missing perms,
  `mkdir summaries/` denied): log Warn with the underlying error,
  return nil from the constructor anyway, and do NOT block
  `alfred-server` startup. Sidebar will show stale content until
  the user navigates away and back; functional but lossy. The
  binary's exit code path is the same as if the watcher hadn't been
  requested. Bookkeeping: the watcher constructor returns `nil,
  nil` on a recoverable failure; main.go just continues.
- **GET /summary 500** (disk read fail): sidebar shows "Couldn't load
  summary: <err message>" — kept visible because the WS keeps trying.
- **GET /template 500**: same pattern in the template view.
- **Template ID unknown** (e.g. user is on a session created with an
  old templateId that we've removed): handleClaudePrompt skips
  injection silently; the sidebar shows the empty state. Log once at
  Info.
- **Watcher fires for a path with a name we don't recognise** (no
  `<sid>.md` pattern): silently skip.
- **summary file > some sane cap (say 100KB)** : serve only the first
  100KB and append `\n\n... [truncated]`. We don't want the sidebar
  re-parsing a 5MB file every keypress.

## Migration

No persisted data migration. New `TemplateID` field is optional in
SessionMeta; existing sessions deserialise with `TemplateID: ""` and
skip injection. Reconcile clears it on Pod restart like Renderer.
Summaries directory is created lazily on first write; deletion of the
data dir is harmless.

## Open implementation questions (none — all decided)

- Inject position: append after user text with `\n\n---\n` ✓
- Storage: `<ALFRED_DATA_DIR>/summaries/<sid>.md` ✓
- Update method: template asks Claude to Read first, then Write ✓
- UI position: right sidebar, always visible in Chat UI ✓
- Editability: read-only viewer ✓
- v1 builtins: one (`summary-todo`), task-list style ✓
- Freshness: fsnotify → WS summary_updated → frontend GET ✓
- Auto-allow: Read AND Write of the canonical summary path; Write
  rejected if content > 8 KB (anti-prompt-injection guard) ✓
- summary_updated frame is per-session, not broadcast ✓
- Sidebar hidden state is global and sticky across session switches ✓
- 200 with empty body treated identically to 404 ✓
- Template instructs Claude to keep the file under 1500 chars ✓

## Future (v1.x+, out of v1 scope)

- More built-in templates: `summary-memo` (conversation style),
  `summary-facts` (KV style), `pinned-cwd-conventions`, etc.
- Per-user template overrides (file-based, mounted from user-managed
  dir).
- Editable templates from the UI (modal editor + save endpoint).
- Multiple concurrent templates on one session (compose into the
  injected suffix).
- Cross-session summary aggregation (a "projects" overview screen).
- Replace polling-style template view with real-time edit collaboration.
