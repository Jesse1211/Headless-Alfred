## Daily recap

A single dedicated "recap" session that asks Claude to summarize the
user's day, by running Claude in headless UI mode and letting it
combine three sources in parallel: superpowers skill suggestions,
claude-mem timeline, and git history. The output is written to a
date-stamped markdown file the user can browse and chat about.

### Why a separate session type

A recap is one short conversation grounded in a fixed prompt against
data sources the user doesn't normally surface in regular chat. It
deserves its own entry point so users don't pollute their working
session's context with summarization work, and so the UI can specialize
around a different right-sidebar (recap browser instead of summary
template).

But: it should still be a real Alfred session under the hood — a tmux
pane, a Claude UI mode runner, a jsonl history that survives refreshes.
We get that for free by tagging it with a new `Kind: 'recap'` flag on
`SessionMeta` and reusing every other piece of the existing Claude UI
machinery.

### User flow

```
┌─ left ──────┐  ┌─ main ──────────────┐  ┌─ right ──────────────┐
│ + New chat  │  │                      │  │  RecapSidebar         │
│ ─────────── │  │                      │  │  ─────────────────    │
│ chat 1      │  │   ClaudeChatView      │  │  [Generate today's    │
│ chat 2      │  │   (regular UI mode,  │  │   recap]              │
│ chat 3      │  │   resumes via        │  │  ─── only when today  │
│             │  │   claude -c)         │  │                       │
│ ─────────── │  │                      │  │  Today · 2026-06-15   │
│ + 复盘      │  │                      │  │  2026-06-14           │
│             │  │                      │  │  2026-06-12           │
└─────────────┘  └──────────────────────┘  └───────────────────────┘
```

Sequence when the user clicks "+ 复盘":

1. Backend: if no recap session exists, create one
   (`Kind: 'recap'`, hidden from sidebar list). If one exists, switch
   to it.
2. Backend: kill any other recap session that was somehow lingering —
   invariant is "at most one alive recap session at a time".
3. Frontend: navigate to the recap session — workspace renders
   `ClaudeChatView` in the middle, `RecapSidebar` on the right.
4. Backend: auto-enter Claude UI mode with `bypassPermissions: true`,
   spawning `claude -c -p --output-format stream-json …` (the `-c`
   continues the most recent conversation, so prior recap chats are
   threaded through even when the underlying Alfred session was
   killed since last time).
5. User sees the chat is empty (just the just-entered Claude prompt
   placeholder) until they either click "Generate today's recap" or
   ask Claude a question manually.

Switching away (selecting any non-recap session) → backend kills the
recap session's tmux pane and removes it from `sessions.json`. The
`.md` files stay; `claude -c` will pick up the prior conversation
next time. A page reload alone does NOT kill the session (the
selection persists in localStorage so the same recap session is
re-entered); only a real selection change to a different sid fires
the cleanup.

### Recap content

Recaps live at:

```
<ALFRED_DATA_DIR>/recaps/<YYYY-MM-DD>.md
```

Naming is by local date at write-time. Concurrent regenerations
overwrite each other (last write wins); we accept this — a recap
generation takes 30-60s, and the user is unlikely to double-click.

The "Generate today's recap" button does NOT directly write the file;
it sends a fixed prompt as a `claude_prompt` WS frame just like any
other user message. Claude (running with all hooks + tools) reads the
prompt, runs its own tool calls, and writes the file itself via
`Write`. The PreToolUse hook still asks for approval as usual, but
under `bypassPermissions: true` it auto-allows (this is the same
mechanic the summary template already uses).

#### Fixed prompt for "Generate today's recap"

The prompt is composed server-side, identical to how
`internal/template/render.go` renders the summary-todo template. A
new template `recap-daily` (key in `internal/template/builtin.go`)
contains:

```
Generate today's daily recap for the user. Today is <date>.

Steps:

1. Before doing anything, check whether any superpowers skills apply
   (e.g. a 'daily-recap' or 'summarize' skill). If one does, invoke
   it and follow its instructions instead of these steps.

2. Otherwise, run these three lookups IN PARALLEL (one Bash call and
   two tool calls in a single response):
   - Bash: `cd <cwd> && git log --since="<date> 00:00" --until="<date> 23:59" --all --pretty=format:"%h %s (%an)"` plus `git diff --shortstat HEAD@{midnight}..HEAD`
   - claude-mem: `mcp__plugin_claude-mem_mcp-search__timeline` for today's slice
   - claude-mem: `mcp__plugin_claude-mem_mcp-search__memory_search` for "today's decisions"

3. Synthesize a markdown recap with this exact structure:

   ```
   # Recap · <date>

   ## Shipped
   - <bullet of concrete output: PR opened, file written, deploy>

   ## Decisions
   - <bullet of judgement calls made, with the why>

   ## Open questions
   - <bullet of unresolved items the user should address tomorrow>

   ## Notes
   - <anything else worth remembering>
   ```

4. Write the result to <recap_path> (overwriting any existing file).
   Use the Write tool; do NOT print the recap inline.

5. Confirm to the user with one short line: "Recap saved to <date>.md.
   Ask me about anything from today."
```

Placeholders `<date>`, `<cwd>`, `<recap_path>` are server-side
substitutions identical to summary-todo's `<sid>` / `<summary_path>`.

#### Auto-allow for the recap file path

`internal/claude/dispatcher_summary.go` has an `isSummaryIO` predicate
that auto-allows Read+Write on the canonical summary file (8 KB cap).
Add a sibling `isRecapIO` for `<DATA_DIR>/recaps/<date>.md`:

- auto-allow Read of any `<DATA_DIR>/recaps/<YYYY-MM-DD>.md` (Claude
  may want to read yesterday's recap as context for today's)
- auto-allow Write of any `<DATA_DIR>/recaps/<YYYY-MM-DD>.md`
- strict path match `<DATA_DIR>/recaps/[0-9]{4}-[0-9]{2}-[0-9]{2}\.md` —
  no traversal, no other extensions
- Write payload capped at 16 KB (recaps are longer than summaries)

The `OnAsk` fast-path lands the auto-allow before the WS-subscriber
lookup, same as summary.

### Recap session lifecycle

`SessionMeta.Kind` is a new field:

```go
type SessionKind string
const (
    KindChat  SessionKind = ""        // default (back-compat with old files)
    KindRecap SessionKind = "recap"
)

type SessionMeta struct {
    // ... existing fields ...
    Kind SessionKind `json:"kind,omitempty"`
}
```

`Manager` invariants (new):

- **At most one recap session lives at a time.** `CreateRecapSession()`
  first scans for existing `Kind == KindRecap` sessions and kills any
  it finds.
- **Recap sessions never appear in `List()` results passed to the
  sidebar.** The list endpoint adds a query filter
  `?kind=chat|recap|all`; default is `chat`. The frontend's
  `SessionsSidebar` calls with default (chat only); the new
  `+ 复盘` entry uses a dedicated endpoint to find-or-create.
- **A recap session is killed via the same `Delete` path** when the
  user navigates away. The frontend triggers the delete via a new
  `?if-recap=1` guard query so navigation never accidentally drops
  a chat session.

### Backend endpoints

| method | path | purpose |
|---|---|---|
| `POST` | `/api/recap-sessions` | Find-or-create the recap session. Returns `{ id, name, claude_session_id, ... }`. Idempotent: returns the existing one if alive. |
| `DELETE` | `/api/recap-sessions/current` | Kills the current recap session (if any). Idempotent — 204 even if no recap session exists. |
| `GET` | `/api/recaps` | Returns `[{ date: "2026-06-15", isToday: true }, ...]` sorted desc; only dates that have a file. |
| `GET` | `/api/recaps/{date}` | Returns the raw markdown for that date. `text/markdown`. 404 if missing. |

`POST /api/recap-sessions` triggers `EnterClaude` with
`renderer: ui`, `bypassPermissions: true`, `templateId: ""` (no
summary template — recap session is its own thing). The frontend
doesn't show the Start Claude dialog for recap sessions.

The Claude invocation uses `claude -c -p ...` (continue). To enable
this, `internal/claude/runner.RunOptions` gains a `Continue bool`
flag; recap sessions pass `Continue: true`. Regular chat sessions
keep using `--resume <uuid>` as today (no change).

### Frontend components

| file | role | new/existing |
|---|---|---|
| `web/src/features/sessions/SessionsSidebar.tsx` | Add `+ 复盘` button at the bottom, next to `+ New chat` | existing |
| `web/src/features/sessions/useSessions.ts` | `createOrEnterRecap()` callback | existing |
| `web/src/features/sessions/types.ts` | add `kind?: 'chat' \| 'recap'` to `Session` | existing |
| `web/src/features/sessions/RecapSidebar.tsx` | new right sidebar — generate button + date list + content viewer | new |
| `web/src/features/sessions/RecapSidebar.css` | styles | new |
| `web/src/features/sessions/WorkspacePage.tsx` | mount `RecapSidebar` instead of `SummarySidebar` when `selected.kind === 'recap'` | existing |
| `web/src/lib/api.ts` | `createRecapSession`, `deleteRecapSession`, `listRecaps`, `getRecap` helpers | existing |

`SessionsSidebar` shows only `kind === 'chat'` sessions. The `+ 复盘`
button is the sole way to enter recap mode.

`useSessions` gains:

```ts
async function createOrEnterRecap() {
  const r = await createRecapSession()      // backend find-or-create
  await setSelectedSessionID(r.id)
  // No StartClaudeDialog — backend has already EnterClaude'd it.
}
```

When the user picks any other session from the sidebar, an effect
fires `deleteRecapSession()` (best-effort, fire-and-forget). The
dispatched DELETE is idempotent.

`RecapSidebar` is mounted alongside `ClaudeChatView` only when
`selectedSession.kind === 'recap'`. It replaces (not supplements)
`SummarySidebar` in that case; the two sidebars never coexist.

### `RecapSidebar` UX details

```
┌─ RecapSidebar ────────────────────────┐
│ Recap                                  │  ← no × — sidebar is intrinsic to recap mode; exit by picking another session
│ ─────────────────────────────────     │
│ [+ Generate today's recap]            │  ← visible only if today not yet generated
│ [↻ Refresh today's recap]             │  ← visible if today already exists
│ ─────────────────────────────────     │
│  Today · 2026-06-15           ●       │  ← currently shown (highlighted)
│  2026-06-14                            │
│  2026-06-12                            │
│  2026-06-10                            │
│                                        │
│ ─── content ─────────────────────     │
│  # Recap · 2026-06-15                  │
│  ## Shipped                            │
│  - ...                                 │
└────────────────────────────────────────┘
```

- Date list and content are stacked vertically inside the sidebar, not
  side-by-side. Sidebar width unchanged from `SummarySidebar` (320px).
- Default selected date is today. If today has no recap yet, the
  content area shows a placeholder: "No recap for today yet. Click
  Generate to create one."
- Clicking a past date loads that file into the content area. The
  "Generate / Refresh" button is hidden because past days are
  read-only.
- Generate button → fires `claude_prompt` with the rendered
  `recap-daily` template. Disabled while a generation is in flight
  (use the existing `claude.inFlight` state).
- After Claude writes the file, the backend's file watcher (same
  fsnotify machinery the summary feature uses) emits `recap_updated`
  WS frames; the sidebar's date list and content refetch.

### WS protocol additions

| frame | when | payload |
|---|---|---|
| `recap_updated` | a file matching `<DATA_DIR>/recaps/*.md` was written | `{ date: "2026-06-15" }` |

The reducer bumps a `recapFetchCounter` per session that the
`RecapSidebar` `useEffect` depends on (same pattern the summary
sidebar uses). Per-WS subscription, not broadcast — but since recap
files are global (not per-session), we send to every connected client
that has a recap session selected. For v1, broadcast to all WS
clients is acceptable; the data is small.

### Storage layout

```
<ALFRED_DATA_DIR>/
├── sessions.json
├── sessions/<sid>/...
├── summaries/<sid>.md         ← existing
└── recaps/<YYYY-MM-DD>.md     ← new
```

### Error handling

| failure | behavior |
|---|---|
| no `<DATA_DIR>/recaps/` dir yet | created lazily on first write by Claude's `Write` tool, also pre-created by alfred-server boot |
| `GET /api/recaps` when dir empty or missing | `200 []` |
| `GET /api/recaps/<date>` when file missing | `404 not_found` |
| invalid date format in URL | `400 bad_request` |
| Claude generation fails (crash, denial) | normal `claude_error` / `claude_run_ended` flow — no recap file written; sidebar still shows old data |
| user spam-clicks Generate | button disabled while inflight; clicks ignored |
| user kills tab during generation | tmux pane stays alive, claude finishes, file lands; next time they open the recap session, the file is there |

### Testing

**Backend Go:**

- `internal/store/sessions_test.go` — round-trip `Kind` field through
  json marshal / unmarshal; absence of `kind` field reads as `KindChat`.
- `internal/session/manager_test.go` — `CreateRecapSession` returns
  existing when one alive; kills duplicates; `List(kind=chat)`
  excludes recaps.
- `internal/api/recap_handlers_test.go` — endpoints, including
  `GET /api/recaps` ordering, date validation, missing file 404.
- `internal/claude/dispatcher_summary_test.go` — extend `isSummaryIO`
  tests with `isRecapIO`; cover path traversal blocked, 16KB cap,
  non-`recaps/` paths denied.
- `internal/template/builtin_test.go` — `recap-daily` template
  renders placeholders.

**Frontend Vitest:**

- `useSessions.test.ts` — `createOrEnterRecap` selects the returned
  session id; `deleteRecapSession` fires on switch-away.
- `RecapSidebar.test.tsx` — generate button visible only on today
  without recap; date list renders; clicking date triggers content
  fetch.
- `sessionsReducer.test.ts` — `recap_updated` frame bumps counter
  globally (no per-session filter).

**Playwright:**

- New describe in `regression.spec.ts`: click `+ 复盘`, observe
  recap session UI loads, click Generate, wait for file appearance
  (`fs.existsSync(recaps/YYYY-MM-DD.md)` poll up to 60s), assert
  sidebar shows the file content. Cleanup deletes the file +
  `DELETE /api/recap-sessions/current`.

### Out of scope

- Multi-recap-per-day (overwriting is acceptable).
- Editing recap content from the UI — read-only file viewer; user
  edits by talking to Claude (re-running Generate).
- Recap notifications / daily reminders.
- Recap sharing / export.
- Cross-session recap context (each recap is independent; `-c`
  continuity is for the chat thread, not the recap content itself).
- Multi-tab live sync of recap session lifecycle. If the user opens
  the workspace in two tabs and clicks `+ 复盘` in both, the second
  click finds the existing session and joins it; navigating away in
  one tab will kill the session out from under the other. Accepted
  trade-off; the alternative (refcount across tabs) is YAGNI.

### File layout

| file | role |
|---|---|
| `internal/store/sessions.go` | add `Kind` field + `SessionKind` type |
| `internal/session/manager.go` | `CreateRecapSession`, `List(kind)` filter, recap-cleanup on side effects |
| `internal/template/builtin.go` | add `recap-daily` template |
| `internal/template/render.go` | extend placeholder set with `<date>`, `<cwd>`, `<recap_path>` |
| `internal/api/recap_handlers.go` | the 4 endpoints |
| `internal/api/router.go` | register routes |
| `internal/claude/dispatcher_recap.go` | `isRecapIO` predicate, wired into `OnAsk` |
| `internal/claude/dispatcher.go` | call `isRecapIO` alongside `isSummaryIO` |
| `internal/claude/runner.go` | `RunOptions.Continue` flag, append `-c` if set |
| `internal/api/claude_handlers.go` | recap sessions invoke runner with `Continue: true` |
| `internal/summary/watcher.go` | refactor to support both summary + recap watchers, OR factor a shared `dirWatcher` helper |
| `web/src/features/sessions/types.ts` | `Session.kind`, `recapFetchCounter` |
| `web/src/features/sessions/sessionsReducer.ts` | handle `recap_updated` |
| `web/src/features/sessions/useSessions.ts` | `createOrEnterRecap`, deleteRecap on switch-away |
| `web/src/features/sessions/SessionsSidebar.tsx` | `+ 复盘` button; filter list to `kind=chat` |
| `web/src/features/sessions/RecapSidebar.tsx` | new |
| `web/src/features/sessions/RecapSidebar.css` | new |
| `web/src/features/sessions/WorkspacePage.tsx` | mount RecapSidebar when `kind=recap` |
| `web/src/lib/api.ts` | 4 helpers |
| `web/src/lib/ws.ts` | `recap_updated` ServerMsg variant |
| `web/e2e/regression.spec.ts` | end-to-end recap flow |
