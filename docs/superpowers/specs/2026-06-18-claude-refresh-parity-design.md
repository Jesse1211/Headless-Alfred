# Claude UI Refresh Parity — Design

**Date:** 2026-06-18
**Status:** Approved (brainstorm phase)
**Branch:** `refactor/refresh-parity`

## Problem

After a page refresh, the Claude UI loses several user-visible fields
the live WebSocket reducer had computed locally: per-tool elapsed
duration, per-tool approval decision (allow/deny), per-turn total cost,
per-turn token usage. The cause is structural — Claude CLI's transcript
file (`~/.claude/projects/<dir>/<uuid>.jsonl`) doesn't record any of
these. The frontend stamped them via `new Date().toISOString()` and
threw them away on reload.

This document defines a refactor that makes the rendered Claude state
**identical before and after a page refresh** for every field the
frontend currently shows. The mechanism: the backend becomes the single
source of truth for runtime state; it persists an `~/.alfred/` JSON
snapshot per session; the frontend hydrates from a single HTTP endpoint
and applies WS events on top of that, never persisting fields locally.

## Goals

- Refresh parity for: `turn.finishedAt`, `turn.totalCostUsd`,
  `turn.usage`, `tool.startedAt`, `tool.finishedAt`, `tool.decision`,
  `turn.done` (precise vs jsonl's "force-true").
- Single source of truth: server. Frontend reducer never computes
  persistable state.
- Atomic on-disk snapshot per session, written via tmp+fsync+rename.
- Graceful degradation when snapshot is missing/corrupt: fall back to
  pure jsonl reconstruction (current behavior).
- No regression for existing chat content (turn skeleton, text, tool
  shape) — jsonl remains the ground truth there.

## Non-goals

- Persisting transient queues (`pending`, `pendingQuestions`,
  `lastError`).
- Persisting external-resource references (`bgTasks`, `subagents`) —
  these reference live OS processes / hook lifecycles that can't be
  validated cold; the server re-emits them via WS on reconnect.
- Backfilling historical turns. Cost/elapsed/decision are
  structurally absent in old jsonl data; we own from now forward.
- Multi-client merge / CRDT semantics. Single user, single tab is the
  assumed model.
- Event sourcing (separate append-only event log). The Claude CLI jsonl
  already plays that role for chat skeleton; adding a second log would
  give us two truth sources.

## Architecture

Server is the single source of truth. Per Alfred session, the server
holds an in-memory `ClaudeState` and persists a JSON snapshot at
`~/.alfred/sessions/<alfred-session-id>/claude.json`.

All state mutations — Claude CLI stream-json events, user tool
decisions, in-flight task updates — converge on a single `Apply(event)`
entry point. `Apply`:

1. acquires the session's write lock,
2. runs the reducer against the in-memory state,
3. marks the session's `Persister` dirty (non-blocking),
4. releases the lock,
5. broadcasts the event to all connected clients over WS.

Two hydration paths produce identical `ClaudeState` shapes:

- **First-load / refresh:** `GET /api/sessions/<id>/claude-state`.
  Server returns a deep-copied snapshot of the in-memory state. If
  the in-memory state hasn't been built yet, it's loaded lazily from
  snapshot + jsonl.
- **Live updates:** existing WS event protocol. Frontend reducer
  applies events incrementally on top of the hydrated state.

Snapshot files are an implementation detail of the server. The
frontend never reads them directly — all access goes through the
HTTP endpoint.

```
                ┌──────────────────────────────────┐
                │  Frontend (web/src)              │
                │                                  │
   GET state ◀──┤  useClaudeStateLoader (HTTP)     │
                │           │                      │
   WS events ◀──┤  useSessions → reducer (events) ◀│── ws frames
                │           │                      │
                │           ▼                      │
                │     ClaudeState (memory only)    │
                └──────────────────────────────────┘
                            ▲
                            │ HTTP json
                            │
                ┌──────────────────────────────────┐
                │  Server (internal/claudestate)   │
                │                                  │
                │   SessionManager                 │
                │       │                          │
                │       ▼                          │
                │   SessionState (Apply, View)     │
                │       │                          │
                │       ▼                          │
                │   Persister (goroutine, dirty)   │
                │       │                          │
                └───────┼──────────────────────────┘
                        ▼
              ~/.alfred/sessions/<id>/claude.json
              (atomic write: tmp + fsync + rename)
```

## Snapshot File Format

Path: `~/.alfred/sessions/<alfred-session-id>/claude.json`

```jsonc
{
  "version": 1,
  "sessionId": "01KVBX535FVFNH6SHF8P5WZ54B",
  "claudeUuid": "f6098eee-81d1-407e-9f42-c836cc5568ab",
  "writtenAt": "2026-06-18T07:14:23.451Z",
  "turns": [ /* []ClaudeTurn — see types.ts mirror */ ]
}
```

Only `turns` is persisted. Everything else in the live `ClaudeState`
is one of:

- **derivable** (`inFlight = !turns[-1].done`)
- **transient** (`pending`, `pendingQuestions`, `lastError`)
- **external-resource reference** (`bgTasks`, `subagents`)

Persisting transient or external-resource state would surface stale
references after a server restart — the standard rule of "only
persist what you can independently validate as fresh" rules it out.

### Schema invariants

- `state.turns[i].id` is unique within a snapshot.
- `state.turns[i].blocks` order is the rendering order; must equal
  the order produced by jsonl replay for the same turn.
- All timestamps are ISO 8601 UTC (`Z` suffix).
- Unknown fields on deserialization are ignored (Go's
  `json.Unmarshal` default). This permits forward-compatible field
  additions without bumping `version`.
- Removing a field or changing its type/semantics requires bumping
  `version`.
- **JSON field naming.** On-disk JSON keys are camelCase
  (`claudeUuid`, `finishedAt`, `totalCostUsd`). Go struct fields
  follow Go style (`ClaudeUUID`, `FinishedAt`, `TotalCostUSD`) and
  carry explicit `json:"..."` tags. TypeScript types mirror the JSON
  shape one-to-one. The wire format (HTTP + WS) uses the same
  camelCase keys as the snapshot file — no name translation between
  the persistence layer and the API layer.

### Fields persisted on `ClaudeTurn`

| Field             | jsonl has it? | Persisted in snapshot |
| ----------------- | ------------- | --------------------- |
| `id`              | yes (uuid)    | yes (synced)          |
| `prompt`          | yes           | yes (synced)          |
| `expandedPrompt`  | yes (split)   | yes (synced)          |
| `startedAt`       | yes (user ts) | yes (synced)          |
| `finishedAt`      | approx        | **yes (authoritative)** |
| `blocks`          | yes           | yes (skeleton + tool fields) |
| `thinking`        | yes           | yes (synced)          |
| `done`            | force-true    | **yes (authoritative)** |
| `isError`         | no            | **yes**               |
| `totalCostUsd`    | no            | **yes**               |
| `usage`           | sums to it    | **yes**               |

### Fields persisted on `ClaudeToolCall`

| Field          | jsonl has it? | Persisted in snapshot |
| -------------- | ------------- | --------------------- |
| `toolUseId`    | yes           | yes (synced)          |
| `name`         | yes           | yes (synced)          |
| `input`        | yes           | yes (synced)          |
| `result`       | yes           | yes (synced)          |
| `isError`      | yes           | yes (synced)          |
| `startedAt`    | **no**        | **yes**               |
| `finishedAt`   | **no**        | **yes**               |
| `decision`     | **no** (always "allow") | **yes** |
| `bgTaskId`     | **no**        | **yes**               |

## Server-side State Manager

New package: `internal/claudestate`. Three roles, single-direction
dependency.

```
HTTP handler / WS handler
        │
        ▼
   SessionManager     ◀──── per-session map + RWMutex
        │
        ▼
    SessionState      ◀──── in-memory ClaudeState + Persister
        │
        ▼
     Persister        ◀──── goroutine + dirty bit + snapshot.json
```

### `SessionManager`

Package singleton, owns the map.

```go
type SessionManager struct {
    mu       sync.RWMutex
    sessions map[string]*SessionState  // alfred session id → state
    rootDir  string                    // ~/.alfred/sessions
}

func (m *SessionManager) GetOrLoad(sessionID, claudeUUID string) (*SessionState, error)
func (m *SessionManager) Snapshot(sessionID string) (ClaudeState, bool)
func (m *SessionManager) Shutdown(ctx context.Context) error
```

First-access load is protected by `singleflight.Group` — concurrent
HTTP requests for the same un-loaded session collapse into one
`Load(...)` call.

### `SessionState`

One instance per active session.

```go
type SessionState struct {
    sessionID  string
    claudeUUID string
    mu         sync.RWMutex
    state      ClaudeState
    persister  *Persister
}

func (s *SessionState) Apply(ev Event) error
func (s *SessionState) View(fn func(*ClaudeState))
func (s *SessionState) Close(ctx context.Context) error
```

`Apply` is the **only** mutation entry point. WS event ingestion,
HTTP-mutating handlers, and hook-driven updates all route through
it. This makes "reducer is a pure function of (event, state)" a
machine-checked invariant rather than a coding convention.

`Apply` returns no delta. The broadcast layer forwards the **same
`Event`** it just handed in; client reducers (web frontend, future
clients) run the same projection logic to reach the same state.
Replicating the input keeps the wire protocol identical to "what
the reducer consumed" — no two-shape risk.

### `Event` taxonomy

`Event` is a tagged union, one source per kind:

| Kind                  | Source                                                                  |
| --------------------- | ----------------------------------------------------------------------- |
| `text_delta`, `tool_use_start`, `tool_use_end`, `tool_result`, `thinking_delta`, `message_start`, `message_delta`, `message_stop`, `result` | Claude CLI stream-json, parsed by `internal/claude/parser.go`. |
| `tool_decision`       | User UI action arriving as a WS frame `tool_decision`.                  |
| `task_started`, `task_notification`, `task_updated` | Background task hooks (Monitor).                                          |
| `hook_started`, `hook_response` | Subagent hooks.                                                          |
| `claude_error`        | Runner failure surfaced by the server.                                  |
| `claude_run_ended`    | Runner process exit (backstop for `finalizeInFlightTurn`).              |

Each kind carries server-stamped `Timestamp time.Time` set at the
moment `Apply` is called. Frontend reducer reads this verbatim into
the corresponding state field; no `new Date()` on the client.

### `Persister`

One goroutine per `SessionState`.

```go
type Persister struct {
    path     string
    dirty    chan struct{}        // buffered cap 1, coalesces signals
    flushReq chan chan error      // synchronous flush
    state    *SessionState
    debounce time.Duration        // 100ms
}

func (p *Persister) Run(ctx context.Context)
func (p *Persister) MarkDirty()
func (p *Persister) Flush(ctx context.Context) error
```

Main loop:

```
for {
  select {
  case <-dirty:
    timer := time.NewTimer(100ms)
    drain(dirty)
    select {
    case <-timer.C:        writeSnapshot()
    case ack := <-flushReq: timer.Stop(); ack <- writeSnapshot()
    case <-ctx.Done():     return
    }
  case ack := <-flushReq: ack <- writeSnapshot()
  case <-ctx.Done():       return
  }
}
```

`writeSnapshot` performs the standard atomic write:

```
1. state.View(s => deep-copy turns)
2. marshal to JSON
3. write to <path>.tmp
4. fsync(<path>.tmp)
5. rename <path>.tmp → <path>            (POSIX atomic on same FS)
6. fsync parent dir                       (crash durability)
```

`os.WriteFile` is insufficient — no fsync, crash leaves zero-length
file. We open with `O_WRONLY|O_CREATE|O_TRUNC`, write, `Sync()`,
`Close()`, then `os.Rename`.

**Deep copy implementation.** Hand-written per-struct, not via
`reflect` or JSON round-trip. Reflect is slow and easy to misuse;
JSON round-trip silently drops fields tagged `json:"-"` (which we
use for internal index maps). Hand-written copies are ~30 lines
total for the entire `ClaudeState` tree and trivial to keep correct.

### `Loader`

```go
// Load reads snapshot.json (if present) and jsonl (always), merges
// them into a ClaudeState per the rules in the Merge Rules section.
func Load(snapshotPath, jsonlPath string) (ClaudeState, error)
```

Lock-free. Returns a fresh `ClaudeState`; caller installs it on a
`SessionState`.

### HTTP handler

New endpoint `GET /api/sessions/{id}/claude-state` returns the full
`ClaudeState`:

```go
func handleClaudeState(w http.ResponseWriter, r *http.Request) {
    sid := chi.URLParam(r, "id")
    cuid := lookupClaudeUUID(sid)  // from internal/session metadata store
    s, err := manager.GetOrLoad(sid, cuid)
    if err != nil { /* 500 */ }
    var snap ClaudeState
    s.View(func(st *ClaudeState) { snap = deepCopy(*st) })
    json.NewEncoder(w).Encode(snap)
}
```

The legacy `GET /api/sessions/{id}/claude-history` endpoint stays one
release cycle with `Deprecation: true` and `Sunset: <date>` headers,
returns only the `turns` field for backward compatibility.

### WS event ingestion

Today: Claude stream-json → parsed `Event` → broadcast verbatim to
clients.

After refactor: Claude stream-json → parsed `Event` → `SessionState.Apply(ev)`
→ broadcast. This guarantees that "server in-memory state ≡ what any
client can reconstruct from the broadcast stream."

### Package layout

```
internal/
  claudestate/                ← new package
    manager.go                SessionManager
    state.go                  SessionState + Apply (reducer)
    persister.go              Persister goroutine
    loader.go                 snapshot + jsonl merge
    types.go                  ClaudeState, ClaudeTurn, ...
    *_test.go
  claudehistory/              ← kept; loader.go calls it for jsonl replay
  api/
    claude_state_handler.go   new endpoint
    claude_history_handler.go ← marked Deprecated
```

## Merge Rules

`Loader.Load` is the only place where snapshot + jsonl combine.

### Turn-level alignment by `id`

```
snapshotTurns := parse(snapshot.json).turns
jsonlTurns    := claudehistory.Parse(jsonl).turns

snapByID := map[string]Turn{}
for _, t := range snapshotTurns { snapByID[t.id] = t }

merged := []Turn{}
for _, jt := range jsonlTurns {
    if st, ok := snapByID[jt.id]; ok {
        merged = append(merged, mergeTurn(jt, st))
    } else {
        merged = append(merged, jt)
    }
}
```

Turns in the snapshot but not the jsonl are **dropped**. Reasoning:
jsonl is Claude CLI's own truth source for chat content; a snapshot
turn the jsonl doesn't know about indicates corruption or external
tampering and shouldn't be trusted.

Turn id comes from the jsonl user-line `uuid` field (already in use).
When absent, `claudehistory.Parse` falls back to `stableID(line)`
(sha1 of line bytes) — deterministic across reparses, so alignment
still works.

### Field-level merge

| Field              | jsonl                  | snapshot               | Merged                        |
| ------------------ | ---------------------- | ---------------------- | ----------------------------- |
| `Turn.id`          | authoritative          | —                      | jsonl                         |
| `Turn.prompt`      | authoritative          | mirror                 | jsonl                         |
| `Turn.expandedPrompt` | authoritative       | mirror                 | jsonl                         |
| `Turn.startedAt`   | authoritative (user ts)| mirror                 | jsonl                         |
| `Turn.finishedAt`  | approx (next user ts)  | **authoritative**      | snapshot, fallback jsonl      |
| `Turn.blocks`      | order + skeleton       | per-tool overrides     | jsonl order; tool field merge |
| `Turn.thinking`    | authoritative          | mirror                 | jsonl                         |
| `Turn.done`        | force-true             | **authoritative**      | snapshot for trailing turn; else true |
| `Turn.isError`     | absent                 | **authoritative**      | snapshot, fallback false      |
| `Turn.totalCostUsd`| absent                 | **authoritative**      | snapshot, omit if absent      |
| `Turn.usage`       | recomputable           | **authoritative**      | snapshot, fallback recompute  |

### `blocks` sub-merge

```go
func mergeBlocks(jsonlBlocks, snapBlocks []AssistantBlock) []AssistantBlock {
    snapToolByID := map[string]ToolCall{}
    for _, b := range snapBlocks {
        if b.Kind == "tool" {
            snapToolByID[b.Tool.ToolUseID] = *b.Tool
        }
    }
    out := make([]AssistantBlock, len(jsonlBlocks))
    for i, b := range jsonlBlocks {
        if b.Kind == "text" {
            out[i] = b
            continue
        }
        merged := *b.Tool
        if snap, ok := snapToolByID[b.Tool.ToolUseID]; ok {
            merged.StartedAt  = snap.StartedAt
            merged.FinishedAt = snap.FinishedAt
            merged.Decision   = snap.Decision
            merged.BgTaskId   = snap.BgTaskId
        }
        out[i] = AssistantBlock{Kind: "tool", Tool: &merged}
    }
    return out
}
```

Text blocks come from jsonl unchanged. Tool blocks take their
skeleton (`name`, `input`, `result`, `isError`) from jsonl and their
extended fields (`startedAt`, `finishedAt`, `decision`, `bgTaskId`)
from snapshot. Tools in the snapshot but not the jsonl are dropped.

### `Turn.done` resolution

`claudehistory.Parse` force-sets `done=true` on every parsed turn —
that's a too-aggressive setting because a runner crash may have left
the trailing turn unfinished. The snapshot's `done` value is precise
(reducer only sets it on `result` or `finalizeInFlightTurn`).

Merge rule: for the trailing turn, use snapshot's `done`; for all
other turns, `done=true` (a successor user-line in the jsonl proves
the turn completed). This also makes `inFlight = !turns[-1].done`
reliable.

### Top-level non-turn fields

| Field                          | Source                              |
| ------------------------------ | ----------------------------------- |
| `inFlight`                     | derived from `turns[-1].done`       |
| `pending` / `pendingQuestions` | empty (server in-memory only)       |
| `bgTasks` / `subagents`        | empty (server in-memory only)       |
| `lastError`                    | empty (transient by design)         |
| `turnsLoaded`                  | server sets `true` after hydrate    |

### Snapshot write semantics

Each persist writes the **entire** `turns[]` array, not a diff. The
rationale: simplicity (no diff bookkeeping), bounded size (typical
session 100-500KB), and atomic-rename pairing (the file is always a
complete, consistent turns set at the time of write). Incremental
logging would only become useful at the multi-MB-per-session scale,
which we don't have today.

## Frontend Changes

Core constraint: **frontend never persists any field locally**. WS
events carry server-authoritative timestamps; the reducer applies
them as-is.

### Removed local writes

In `web/src/features/sessions/claudeReducer.ts`:

| Before                                            | After                                 |
| ------------------------------------------------- | ------------------------------------- |
| `tool_use_start` → `startedAt: new Date()...`     | event payload carries `started_at`    |
| `tool_result` → `finishedAt: new Date()...`       | event payload carries `finished_at`   |
| `result` → `finishedAt: new Date()...`            | event payload carries `finished_at`   |
| `finalizeInFlightTurn` → `finishedAt: new Date()` | server emits a dedicated event        |
| `task_started` / `hook_started` → `new Date()`    | payloads carry `started_at`           |

Server stamps `time.Now().UTC()` once at `Apply` time and propagates
that single timestamp through both the broadcast payload and the
snapshot. Refresh-hydrated and live-streamed timestamps are
bit-identical.

### Hydration path

Replace `useClaudeHistoryLoader.ts` with `useClaudeStateLoader.ts`:

```ts
const state = await getClaudeState(sessionID)
setPerSession(prev => {
    const next = new Map(prev)
    const cur = next.get(sessionID) ?? emptyPerSessionState()
    next.set(sessionID, { ...cur, claude: state })
    return next
})
```

No local merge — server is authoritative, replaces in full. The
old loader's "preserve local turns if present" branch goes away;
the server reconciles via lock-protected `Apply`.

### Reducer's remaining responsibilities

1. Project WS events onto in-memory `ClaudeState`.
2. Manage purely transient UI flags (`turnsLoaded`, internal index
   maps used during streaming) that don't persist.

### Optimistic UI with reconciliation

User-initiated actions remain optimistic; broadcast events confirm /
correct:

- **Send prompt**: `beginClaudeTurn` pushes a placeholder turn with a
  client-generated `clientNonce` (random 16-char string). The
  `claude_prompt` WS frame already carries an `opts` field; we add
  `clientNonce` to it. Server stores the nonce on the new turn it
  creates in `Apply`, and the broadcast `turn_started` event carries
  `{ clientNonce, turnId }` so the frontend reducer can swap the
  placeholder by nonce match.
- **Tool decision**: `resolveClaudeTool` optimistically sets
  `decision`; server's broadcast event `tool_decision_applied`
  carries `{ toolUseId, decision }` to confirm (or, in failure, override).

This is the standard pattern in Linear / Figma / Discord — optimistic
UI without breaking server-as-truth.

### WS frames introduced or modified

- **Modified**: `claude_event` payloads gain a `timestamp` field
  carrying the server's `Apply`-time wall clock for the event. The
  frontend reducer reads this into the corresponding state field
  (`startedAt`, `finishedAt`, etc.) instead of calling
  `new Date()`. Existing fields are unchanged.
- **Modified**: `claude_prompt` (client → server) accepts a
  `clientNonce` string in its `opts` field.
- **New**: `turn_started` (server → client) — `{ clientNonce,
  turnId, startedAt }`. Sent immediately after `Apply` creates the
  new turn server-side.
- **New**: `tool_decision_applied` (server → client) —
  `{ toolUseId, decision }`. Sent after `Apply` processes a
  `tool_decision` from the client.

All other frames keep their current shape.

### Type sync

`web/src/features/sessions/types.ts` mirrors `internal/claudestate/types.go`.
Manual sync this round; OpenAPI / TS codegen is a future YAGNI.

Internal reducer fields (`_blockIndexMap`, `_thinkingIndexMap`) stay
on the type with an `@internal` JSDoc comment and `json:"-"` on the
Go side so they don't appear in the wire format.

### Test changes

- `claudeReducer.test.ts` — refactored to feed explicit `started_at` /
  `finished_at` on payloads. No more wall-clock assertions like
  `expect(parsed).toBeLessThan(t0 + 2000)`. Tests are now
  deterministic.
- New `useClaudeStateLoader.test.ts` — covers hydrate success, HTTP
  error (preserves local state, sets lastError), 404 (empty state).

## Failure Modes & Migration

### Failure matrix

| Scenario                                | Behavior                                            | User-visible impact                         |
| --------------------------------------- | --------------------------------------------------- | ------------------------------------------- |
| snapshot missing                        | Loader uses pure jsonl; WARN log                    | chat intact; cost/elapsed/decision blank    |
| snapshot JSON parse fails               | same; WARN "snapshot corrupted"                     | same                                        |
| snapshot version > 1                    | same; WARN "snapshot from newer version"            | same                                        |
| snapshot.sessionId mismatch dir         | same; WARN "sessionId mismatch"                     | same                                        |
| snapshot present, jsonl gone            | return snapshot.turns directly                      | full recovery                               |
| both missing                            | empty ClaudeState                                   | normal "new session"                        |
| jsonl line malformed                    | existing Parse skip+warn                            | individual turn may be incomplete           |
| Persister write fails (disk full / perm)| log ERROR, continue, retry on next dirty            | live OK; recent state may not survive crash |
| process SIGKILL mid-write               | `.tmp` orphan; rename didn't happen; old file fine  | up to 100ms of state lost                   |
| process panic mid-write                 | same                                                | same                                        |
| Persister goroutine hangs               | Shutdown's `context.WithTimeout(5s)` force-kills    | rare; last snapshot lost                    |
| two server processes same session       | second Persister's flock fails; FATAL + refuse serve| user error                                  |
| Claude UUID rotation (/compact)         | snapshot.claudeUuid updated; jsonl path uses new id; turn ids stable across rotation | no impact |
| frontend hydrate 5xx / network          | preserve local state if any; set lastError         | banner "history unavailable"; existing turns kept |
| frontend hydrate timeout                | same                                                | same                                        |

### Lifecycle

```
First write:
  mkdir -p ~/.alfred/sessions/<id>/ (mode 0700)
  write claude.json.tmp; fsync; rename → claude.json; fsync parent

Orphan cleanup:
  SessionManager startup walks ~/.alfred/sessions/*/claude.json.tmp
  and deletes them (no "in-use" .tmp by invariant — rename is atomic)

Session release:
  Persister.Flush; Persister.Close; flock release
  in-memory state removed from SessionManager.sessions
  on-disk snapshot kept (reused next entry)
```

### Migration

This is a new feature; no existing data to migrate. First-run behavior
on existing sessions:

1. User enters a session → `SessionManager.GetOrLoad` finds no
   snapshot.
2. `Loader.Load` falls back to pure jsonl reconstruction (today's
   behavior).
3. **No snapshot is written eagerly.** Writing immediately would
   persist all-zero values for old turns, locking in the absent
   fields.
4. As the user takes a new turn, the reducer stamps new fields →
   Persister marks dirty → 100ms later, snapshot lands containing
   the new turn (full fields) and previous turns (skeleton from
   jsonl, no cost/elapsed/decision).

**Consequence:** historical turns from before this refactor will
never carry cost / elapsed / decision. Structural limitation; we
don't fabricate the data.

### Schema evolution

- **Adding a field** (forward-compatible): add it to the Go struct
  and TS type; do not bump version. Old snapshots deserialize with
  the new field as zero value.
- **Removing a field, changing type, changing semantics**: bump
  `version`. Old snapshots are treated as "corrupted" → fall back to
  jsonl. No migration function is written (YAGNI). The user
  re-accrues fields in the new schema by using the session normally.

### Observability

Server-side logs:

| Level | Event                                                       |
| ----- | ----------------------------------------------------------- |
| INFO  | snapshot loaded `sessionID=...` `turns=N`                   |
| INFO  | snapshot written `sessionID=...` `turns=N` `size=N` `durMs=N` |
| WARN  | snapshot missing; falling back to jsonl                     |
| WARN  | snapshot corrupted `err=...`; falling back to jsonl         |
| WARN  | snapshot version mismatch `want=1 got=N`; falling back      |
| ERROR | snapshot write failed `err=...`                             |
| FATAL | snapshot flock failed; another process holds the lock       |

No metrics — Alfred ships no Prometheus / OTLP sink; logs suffice.

### Disk usage

Typical 1-5 KB per turn (with input/output previews). 100-turn
session = 100-500 KB. Bundled under the existing `diskwatcher`
package's coverage of `~/.alfred/`; no new quota.

### Backward compatibility

`GET /api/sessions/<id>/claude-history` survives one release cycle
with `Deprecation: true` / `Sunset: <date>` headers, returning only
the `turns` field. Removed in the following release.

## Testing

The whole design serves five invariants. Tests are organized around
those invariants, not the file structure.

### Invariants

1. **Refresh parity.** For any event sequence `E`, the reducer's
   in-memory state `S1` equals the state reconstructed by `Loader.Load`
   over (snapshot, jsonl) for the same `E`. Deep-equal modulo derived
   fields.
2. **Atomic snapshot.** The snapshot file on disk is always either a
   complete valid JSON snapshot or absent. No half-written state.
3. **Server-as-time-source.** All `startedAt` / `finishedAt` come
   from server `time.Now()` at `Apply` time. No frontend or
   jsonl-replay timestamps in persisted fields.
4. **Jsonl as skeleton truth.** Turn `prompt`, `blocks` order, text
   content come from jsonl. Snapshot can override extended fields
   but cannot introduce turns or change content.
5. **Graceful degradation.** Every failure path returns a non-nil
   `ClaudeState`. Chat skeleton always survives.

### Test matrix

**A. Unit (Go) — `internal/claudestate/`**

- `state_test.go`: Apply per-event-kind state transitions; multi-message
  block ordering (regression for the bug fixed earlier); optimistic
  turn reconciliation.
- `loader_test.go`: snapshot missing/present/corrupt/version-mismatch
  paths; snapshot-only with no jsonl; jsonl-only with no snapshot;
  snapshot-extra-turn and snapshot-extra-tool dropping; trailing turn
  `done` resolution.
- `persister_test.go`: dirty → 100ms write; debounce collapses bursts;
  Flush blocks until write; post-Close MarkDirty is no-op; disk-full
  doesn't panic; mid-process panic leaves no orphan tmp; flock
  conflict.

**B. Integration (Go) — `claudestate/integration_test.go`**

This is the critical layer for invariant #1.

```go
func TestRefreshParity_GoldenPath(t *testing.T) {
    events := loadFixture("multi_turn_with_tools.events.jsonl")

    // Live path
    s1 := NewSessionState(...)
    for _, e := range events { s1.Apply(e) }
    liveState := s1.View(deepCopy)
    s1.Persister.Flush(ctx)

    // Refresh path
    s2, _ := Load(s1.Persister.path, s1.jsonlPath)

    assert.Equal(t, liveState, s2)
}
```

Fixtures cover: single text turn; single tool allowed; single tool
denied; Monitor with bgTask linkage; multi-turn cross-message; thinking
blocks; `/compact` UUID rotation; runner-crash trailing turn.

**C. End-to-end (Playwright) — `web/e2e/refresh-parity.spec.ts`**

1. Send prompt → wait for turn done.
2. Assert visible cost / tokens / elapsed.
3. `page.reload()`.
4. Wait for hydration.
5. **Assert visible strings match pre-reload exactly.**

Coverage: simple text turn cost survives; Bash tool elapsed survives;
denied tool status survives; in-flight turn refresh shows the correct
in-flight or error state.

**D. Concurrency**

- 100 goroutines GetOrLoad same session → 1 Load call (singleflight).
- 1000 concurrent Apply, no races (`go test -race`).
- HTTP snapshot + reducer Apply interleaved 10K times, no races.

**E. Stress (optional)**

- 1-hour synthetic stream: snapshot file grows linearly, not
  exponentially.
- 10K-turn session: `Load` completes in < 1 second.

### Out of scope

- Schema migration function tests (none exist; YAGNI).
- Cross-process sync correctness (flock fail-fast is the only path
  tested).
- Cross-timezone timestamp parsing (UTC end-to-end).
- Assertions that transient state (`lastError`, `pending`) "disappears"
  on refresh — that's a UX expectation, not a state invariant.

### Fixture generation

Under `internal/claudestate/testdata/<name>.{jsonl,snapshot.json,events.jsonl}`:

- `.jsonl`: Claude CLI–style transcript
- `.snapshot.json`: matching snapshot
- `.events.jsonl`: reducer's event view, used in integration tests

A `genfixture` Go command runs the reducer over a scripted sequence
and dumps all three. Hand-writing jsonl by hand is brittle; the
fixture generator keeps them in sync.

### CI

Existing pipeline runs `go test ./...` (already with `-race`),
`vitest`, and `playwright`. New tests slot in. No new CI jobs.

## Risks & Open Questions

- **Reducer logic duplication.** Today Claude state lives in two
  reducers (server WS handler + frontend reducer). This design
  centralizes mutation server-side, but if a third client emerges
  (CLI? mobile?) it still benefits — the source of truth is one
  Go function. No real risk here, just calling out the design intent.
- **Migration of in-flight sessions on first deploy.** If a user is
  mid-conversation when the new server binary starts, their
  `SessionState` initializes from the existing jsonl (no snapshot
  yet). Some recent live events may not have been captured anywhere
  (the old server held them only in WS broadcast scope). Acceptable
  — refresh becomes a no-op cost-of-business for the first reload
  after the upgrade.
- **flock portability.** `syscall.Flock` is POSIX. macOS and Linux
  both support it. Windows is not currently a target; if it becomes
  one, replace with `os.OpenFile(O_EXCL)` or a Windows-specific
  lock primitive.

## Plan Hand-off

Once approved, this design hands off to the `writing-plans` skill to
produce a phased implementation plan with explicit acceptance criteria
per phase.

Suggested phasing:

1. **Types + skeleton.** New `internal/claudestate` package; define
   `ClaudeState`, `ClaudeTurn`, `ClaudeToolCall`, `Event` union with
   JSON tags. No behavior. Verify it round-trips against a hand-written
   snapshot fixture.
2. **Persister.** Goroutine + dirty bit + atomic write + flock +
   orphan-tmp cleanup. Tests cover debounce, Flush, crash resistance.
3. **SessionState.Apply.** Port reducer logic from
   `claudeReducer.ts` to Go. Server stamps timestamps. Tests cover
   every event kind + the multi-message ordering regression.
4. **Loader + merge rules.** Implement field-level merge per the
   matrix in this spec. Build the integration-test fixture generator.
   Add `TestRefreshParity_GoldenPath` and its fixtures.
5. **SessionManager + HTTP endpoint.** Wire `GetOrLoad`, expose
   `GET /api/sessions/{id}/claude-state`. Verify HTTP returns
   identical bytes to a hand-snapshot of the in-memory state.
6. **WS event routing.** Existing ingestion path now goes through
   `Apply`. Add `clientNonce` to `claude_prompt`, introduce
   `turn_started` and `tool_decision_applied` frames. Verify all
   timestamps in broadcast payloads come from `Apply`-time.
7. **Frontend switch.** Replace `useClaudeHistoryLoader` with
   `useClaudeStateLoader`. Remove every `new Date().toISOString()`
   from the reducer; read timestamps from payloads. Update reducer
   tests to feed explicit ts. Add refresh-parity Playwright test.
8. **Cleanup.** Mark `claude-history` endpoint deprecated; remove in
   the following release.
