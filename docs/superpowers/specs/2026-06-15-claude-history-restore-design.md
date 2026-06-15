## Restore Claude UI chat history on refresh

When the user reloads the page (or opens the workspace in a new tab) while
a session is in Claude UI mode, the chat view goes blank. The
conversation continues on Claude's side — `claude -p --resume <uuid>`
still has full context — but the frontend's `perSession[sid].claude.turns`
array was only ever populated by live `claude_event` WS frames, which
don't replay on reconnect.

This spec restores the chat history by reading Claude CLI's own
per-session jsonl file (`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`)
as the source of truth on cold start. Live updates continue to flow
through the existing WS path; the jsonl is only re-read on
session-select.

### Why this approach

The jsonl file is the canonical, append-only log Claude CLI itself uses
to maintain `--resume` continuity. Re-implementing turn persistence in
alfred-server would duplicate it and risk drifting out of sync with what
Claude actually remembers. Reading the jsonl trades a small dependency
on Anthropic's internal file format for zero persistence work on our
side.

If Anthropic changes the format we adapt; the parser is intentionally
defensive (unknown line types are silently skipped, partial reads on
malformed jsonl return whatever was parsed) so the worst-case failure
mode is "history empty, conversation continues from Claude's side"
rather than a crash.

### Architecture

```
~/.claude/projects/<some-dir>/<uuid>.jsonl   ← Claude CLI writes
                  │
                  │ read on demand
                  ▼
        internal/claudehistory          (new Go package)
         ├─ Locate(uuid)  → path        (walks ~/.claude/projects)
         └─ Parse(path, limit, before) → []Turn
                  │
                  │
GET /api/sessions/{sid}/claude-history?limit=100[&before=<turnId>]
                  │
                  │ JSON
                  ▼
   useClaudeHistoryLoader (TS)          (new hook, parallel to existing)
                  │
                  │ seeds perSession[sid].claude.turns
                  ▼
            ClaudeChatView              (unchanged)
```

After the initial seed, the live `claude_event` WS path keeps appending
new turns the same as today. The jsonl is just for cold start.

### File location

Claude CLI stores each session at:

```
~/.claude/projects/<dir>/<uuid>.jsonl
```

`<dir>` is derived from the cwd Claude was invoked in (an encoding of
`/` and `.` to `-`), but we deliberately do not compute it.

**Why not compute the directory.** alfred-server picks its claude
invocation cwd dynamically (`/home/alfred` on the pod, `$HOME` on a
dev box; see `claudeInvocationCWD()` in `internal/api/claude_handlers.go`).
That cwd is never persisted into SessionMeta. A compute-then-stat
strategy would work only on a stable single host, and would silently
fall through to a walk anyway after any environment change — making
compute optimistic dead code most of the time. Walking is also
already fast enough (see below), so the compute path costs complexity
without saving real time.

**Strategy: walk `~/.claude/projects` for `<uuid>.jsonl`.** `Locate(uuid)`
runs `filepath.Walk` and returns the first file whose basename is
`<uuid>.jsonl`. Measured ~80ms across 752 existing jsonl files locally;
a refresh runs this at most once per session (then it's cached).

A small in-memory cache `map[sessionID]string` (mutex-guarded) skips
the walk on repeat calls. The cache:

- is keyed by sessionID, not uuid, so it follows the SessionMeta even
  if `ClaudeSessionID` rotates.
- is invalidated when the cached path no longer stats — handles the
  case where the jsonl was moved or the user re-entered Claude with
  a fresh uuid (the old cached entry would now point at a stale path).
- lives for the lifetime of the alfred-server process; does not
  need persistence.

If the walk turns up nothing → `os.ErrNotExist`. The handler returns
`[]`; empty history is a valid state.

### jsonl line types we care about

Of the ~9 distinct top-level `type` values observed in real jsonl
files, only four contribute to user-visible turns:

| jsonl line | what we do |
|---|---|
| `type: "user"`, `message.content` is a **string** | Start a new `Turn` with `prompt = content` |
| `type: "user"`, `message.content[].type == "tool_result"` | Find the matching tool in the current turn (by `tool_use_id`), set its `result` and `isError` |
| `type: "assistant"`, `message.content[].type == "text"` | Append `text` to the current turn's `text` |
| `type: "assistant"`, `message.content[].type == "tool_use"` | Append a `ClaudeToolCall{id, name, input, decision: 'allow'}` to the current turn's `tools`. **Known lossy reconstruction:** a tool that was actually denied by Alfred's PreToolUse hook also shows up here with `decision: 'allow'`. The denial is reflected in the matching `tool_result` (`is_error: true`, content like "user rejected"), so the user can still tell — but the `decision` field on the rebuilt turn doesn't distinguish. We accept this; the alternative is fragile string heuristics on the result content. |

Everything else (`attachment`, `system`, `permission-mode`, `ai-title`,
`last-prompt`, `file-history-snapshot`, `queue-operation`, and any
future type) is silently skipped.

A `Turn` is sealed (`done: true`) when the next `type: "user"` with a
string content arrives — that begins the next turn. The trailing turn
is left `done: true` as well; partial in-flight turns won't be a
concern since the jsonl is read on refresh, not during a live stream.

Edge case: an assistant or `tool_result` line that arrives before any
user-string line (rare — implies a CLI-injected prelude turn) is
silently dropped. Without a `prompt` we have nothing useful to render;
the assistant text would be orphaned.

Sample shapes (from a real session):

```json
{"type":"user","message":{"role":"user","content":"分析一下这个项目"},
 "uuid":"...","timestamp":"2026-06-11T13:11:07.478Z","sessionId":"..."}

{"type":"assistant","message":{"role":"assistant","content":[
  {"type":"text","text":"I'll invoke the superpowers skill first."},
  {"type":"tool_use","id":"toolu_01MK...","name":"Skill",
   "input":{"skill":"using-superpowers"}}
]}}

{"type":"user","message":{"role":"user","content":[
  {"type":"tool_result","tool_use_id":"toolu_01MK...",
   "content":"...","is_error":false}
]}}
```

### Backend package: `internal/claudehistory`

Three files, one external interface.

**`types.go`** — Go mirror of TS `ClaudeTurn`/`ClaudeToolCall`, so the
handler can `json.Marshal` straight to a shape the frontend reducer
already understands. Field tags use the existing camelCase names
(`startedAt`, `toolUseId`, etc.) to match `web/src/features/sessions/types.ts`.

**`locate.go`** — `Locate(sessionID, uuid string) (string, error)` and
an internal `sync.Map` cache. Walks `<homeDir>/.claude/projects` for
a basename match. `homeDir` is `os.UserHomeDir()` at startup (tests
override via `t.Setenv("HOME", ...)`). Cache entry is invalidated if
its stored path no longer stats.

**`parse.go`** — `Parse(path string, limit int, beforeTurnID string) ([]Turn, error)`:
- Opens the file, scans line by line with `bufio.Scanner` (set
  `Buffer(..., 1<<20)` so 1 MB lines don't fail — tool_result content
  can be large).
- For each line, unmarshal into a minimal struct just enough to read
  `type` and `message.content`, then branch.
- Builds turns into a slice.
- Pagination: if `beforeTurnID` is empty, returns the last `limit`
  turns. If `beforeTurnID` is set, returns the `limit` turns whose
  IDs strictly precede it in the sequence.
- `limit` is clamped server-side to `[1, 500]`.

Errors:
- `os.IsNotExist` from Locate → handler returns `[]`.
- Parse error mid-file → return turns parsed so far + log a warning.
  Partial is better than zero.
- Empty file → returns `[]`.

### API: `GET /api/sessions/{sid}/claude-history`

```
GET /api/sessions/{sid}/claude-history?limit=100[&before=<turnId>]
Authorization: Bearer <token>

200 OK
Content-Type: application/json
[
  {
    "id": "...",
    "prompt": "...",
    "startedAt": "2026-06-11T13:11:07.478Z",
    "text": "...",
    "tools": [
      {"toolUseId":"...","name":"Read","input":{...},
       "decision":"allow","result":"...","isError":false}
    ],
    "done": true
  },
  ...
]
```

Behaviour:
- If session unknown → `404` (consistent with existing handlers).
- If session known but `ClaudeSessionID` empty (user never entered
  Claude UI) → `200 []`.
- If jsonl not found by either Locate strategy → `200 []` + log
  `slog.Warn("claude history jsonl missing", "sid", ..., "uuid", ...)`.
- If jsonl parse mid-fails → `200` with the turns parsed up to the
  failure + log warn.
- If anything else explodes → `500 { "code":"history_error" }`.

`limit` default 100, max 500, min 1. `before` is an optional turn ID
to scroll backwards (YAGNI guard: implemented but the frontend may
not use it in the first cut).

### Frontend: `useClaudeHistoryLoader`

A new hook in `web/src/features/sessions/`, mounted alongside the
existing `useSessionHistoryLoader` in `WorkspacePage`:

```ts
useClaudeHistoryLoader({
  selectedSessionID: s.selectedSessionID,
  perSession: s.perSession,
  setPerSession: s.setPerSession,
})
```

Effect dependencies: `[selectedSessionID, perSession.get(sid)?.mode, perSession.get(sid)?.renderer]`.

Body:
1. Bail if no `selectedSessionID`.
2. Bail if `ps?.mode !== 'claude'` or `ps?.renderer !== 'ui'`.
3. Bail if `ps?.claude?.turnsLoaded === true`.
4. `getClaudeHistory(sid)` → seeds `perSession[sid].claude` with
   `{ turns: response, turnsLoaded: true, inFlight: false, pending: [], pendingQuestions: [] }`.
5. Standard `alive` cancellation pattern.

New flag on `ClaudeState`: `turnsLoaded?: boolean`. Default-false in
`emptyClaudeState()`. Set true after a successful jsonl seed.

**Lifecycle:**
- A successful fetch (incl. an empty array — no history is still a
  valid "loaded" state) sets `turnsLoaded: true`.
- An error also sets it true, to avoid retry loops. The frontend shows
  the "history unavailable" banner once and stays put.
- `claude_exited` clears `turnsLoaded` back to false. Reason: Alfred
  may have rotated the underlying `ClaudeSessionID` between exit and
  the next enter (the backend's reset paths in `manager.go` blank
  `meta.ClaudeSessionID` under some conditions), in which case the
  next enter will resolve to a different jsonl. A refetch on re-enter
  is cheap and idempotent for the common case (same uuid → same jsonl,
  same turns).
- The flag is page-lifecycle, not WS-lifecycle. WS reconnects (network
  flap) do NOT clear it — the cached turns are still valid.

New API helper: `getClaudeHistory(sid: string, opts?: { limit?: number; before?: string }) => Promise<ClaudeTurn[]>` in `web/src/lib/api.ts`.

### Reconciliation with live WS

Sequence on a refresh during an in-progress session:

```
T0  Page loads, WS connects, idle frame arrives with mode=claude, renderer=ui
    → reducer initialises claude: emptyClaudeState() (turns:[], turnsLoaded:false)

T1  useClaudeHistoryLoader fires, fetches /api/.../claude-history
    → response: [turn1, turn2, ... turnN]
    → setPerSession seeds claude.turns = [...]; turnsLoaded = true

T2  User types a new prompt
    → claude_prompt WS frame → backend runs `claude -p --resume <uuid>`
    → beginClaudeTurn appends turnN+1 to claude.turns (optimistic)
    → claude_event frames stream in and mutate turnN+1
    → meanwhile the jsonl also grows, but no one re-reads it

T3  User refreshes again
    → Same as T0/T1; now the jsonl contains turn1..turnN+1, all show up
```

The hybrid is intentional: jsonl on cold start, WS for live. No
multi-tab live sync (out of scope — single-tab works fine; the second
tab gets fresh history on its own refresh).

### Error UX

If `getClaudeHistory` rejects with anything other than 404 (session
gone) or `[]` (no history yet), the frontend renders the existing
empty chat view plus a small dim banner: "Claude history unavailable."
The reducer still sets `turnsLoaded: true` so we don't retry-loop.

### Testing

**Backend unit (Go):**

- `parse_test.go` fixtures under `internal/claudehistory/testdata/`:
  - `simple.jsonl` — one user, one assistant text reply
  - `tool_use.jsonl` — user, assistant with tool_use, user tool_result, assistant text
  - `multi_turn.jsonl` — three full turns with mixed content
  - `unknown_types.jsonl` — interleaved permission-mode, attachment, ai-title lines (asserted skipped)
  - `malformed_mid.jsonl` — two good turns then an invalid line (parse returns the two, logs warn)
  - `empty.jsonl` — zero bytes (returns [])
  - Pagination: `before=<turnId>` returns turns strictly before that id; default returns last N.

- `locate_test.go` (uses `t.Setenv("HOME", tmpDir)` + a fixture tree
  `tmpDir/.claude/projects/whatever/<uuid>.jsonl`):
  - Walk finds the file by basename match.
  - Returns `os.ErrNotExist` when the uuid is missing.
  - Cache returns the same path on repeated calls (no re-walk).
  - Cache invalidates when the cached path is unlinked between calls.

**Frontend unit (Vitest):**

- Extend `useSessions.test.ts` style. `useClaudeHistoryLoader.test.ts`:
  - Fetches on mount with claude+ui session; seeds turns.
  - No fetch when mode is shell.
  - No fetch when already `turnsLoaded`.
  - Cleanup cancels pending fetch on unmount.

**E2e (Playwright):**

- New describe in `regression.spec.ts`: enter Claude UI, send "reply
  with 'k'", wait for response, reload page, assert prompt + reply
  both still visible. Cleanup deletes the synthetic history.
- Uses the real Claude API path (no way to fake jsonl writes
  end-to-end without also faking the runner). `test.setTimeout(60_000)`
  to absorb the API round-trip, same as the existing
  `Claude UI: AskUserQuestion` test. Acknowledged: this test will be
  slow and occasionally flaky on network hiccups, like the other
  Claude e2e tests.

### Out of scope (explicit YAGNI)

- Multi-tab live sync. Single-tab works; second tab refreshes on its
  own.
- Re-parsing jsonl on every WS bump. Hybrid only.
- Editing or deleting history.
- Backfilling sessions that pre-date this feature — they start
  showing up on the next refresh after a fresh turn.
- Streaming the response (chunked transfer) — `limit=100` keeps the
  payload bounded.
- Showing CLI internal events (`permission-mode`, `attachment`, etc.)
  in the chat view — silently skipped.
- Cross-environment history portability. A session that ran in the
  pod (cwd `/home/alfred`) generates a jsonl under
  `/home/alfred/.claude/projects/...`. If you copy ALFRED_DATA_DIR
  to a local machine and refresh there, the `~/.claude/projects/`
  tree the walk searches is the local user's, not the pod's — so the
  jsonl won't be found. Handled gracefully (empty history banner);
  not a supported migration path.

### File layout

| file | responsibility |
|---|---|
| `internal/claudehistory/types.go` | Turn / ToolCall structs with JSON tags matching TS |
| `internal/claudehistory/locate.go` | path computation + walk fallback + cache |
| `internal/claudehistory/parse.go` | jsonl → []Turn |
| `internal/claudehistory/*_test.go` | unit tests |
| `internal/claudehistory/testdata/*.jsonl` | fixture files |
| `internal/api/claude_history_handler.go` | HTTP handler |
| `internal/api/router.go` | route registration (existing file) |
| `web/src/lib/api.ts` | `getClaudeHistory` helper (existing file) |
| `web/src/features/sessions/useClaudeHistoryLoader.ts` | new hook |
| `web/src/features/sessions/types.ts` | add `turnsLoaded?: boolean` to ClaudeState (existing file) |
| `web/src/features/sessions/claudeReducer.ts` | clear `turnsLoaded` on `claude_exited` (existing file) |
| `web/src/features/sessions/WorkspacePage.tsx` | mount the new hook (existing file) |
| `web/e2e/regression.spec.ts` | refresh-restores-history e2e (existing file) |
