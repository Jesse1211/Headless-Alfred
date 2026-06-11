# Multi-session via tmux — design

Single-user Headless Alfred today runs one persistent bash. This spec
extends it to N concurrent shells multiplexed by a tmux server, with a
"chat tabs" sidebar UI. Sessions survive Go-process restarts; they do
not survive Pod restarts (acceptable trade-off).

This spec assumes familiarity with the existing design at
`docs/superpowers/specs/2026-06-11-headless-alfred-design.md` and the
project's three invariants (CONTEXT.md). The first invariant —
"bash lifecycle ≠ WebSocket lifecycle" — is **strengthened** here:
bash lifecycle now also ≠ Go-process lifecycle.

## 1. Goals & non-goals

### Goals

1. Up to 8 concurrent bash sessions, each independently navigable in
   the UI.
2. All sessions share the same filesystem (same container, same Pod).
   `mkdir x` in Session A is immediately visible from Session B.
3. Each session has its own cwd, env, shell history, aliases — like
   independent terminal tabs.
4. **Go-process restart does not kill bash sessions or interrupt
   in-flight commands.** `kubectl rollout`, `go run` restart in dev,
   alfred-server SIGTERM and respawn — none of these terminate user
   work.
5. Sessions can be renamed by the user; the rename persists.
6. Closing a session permanently deletes it (`kill tmux session` +
   `rm -rf` the session's command history).
7. The chat UI from the previous iteration (ChatGPT-style stream)
   continues to work — same per-session bubbles, separators, composer.

### Non-goals

- **Pod restart survivability.** When the container is destroyed
  (rollout, eviction, node failure) tmux dies with it. The persistent
  per-command JSON history survives on the PVC, but the in-flight bash
  state (cwd, env, currently-running command) is lost. Future work
  could mount the tmux socket onto a PVC and accept the cross-Pod
  socket-coordination cost; out of scope here.
- **Multi-user / RBAC.** Still single-user.
- **TTY-app support (vim, htop).** Still out of scope; sentinel
  framing remains line-oriented. The UI is "command + output", not
  an embedded terminal emulator.
- **Cross-tab session selection sync.** Each browser tab tracks its
  own selected session in localStorage, like independent terminal
  windows. See §6.
- **Session-level RBAC / quotas / billing.** Single-user tool.

## 2. The unchanged core

Three things from the previous design stay intact:

- **Sentinel framing.** `Wrap(nonce, cmdID, userCmd)` continues to
  bracket each user command with `\x1eALFRED_START_...` /
  `\x1eALFRED_END_...` printf lines. The parser already deals with
  partial sentinels split across reads (`TestSentinelParser_*`).
- **The `Shell` event surface.** `Started` / `Chunk` / `Ended` events
  on a per-Shell `EventBroadcaster`. Consumers (WS handler,
  disk-writer) don't care what's behind the Shell.
- **JSON-file persistence per command.** One metadata JSON, one raw
  log file per command, atomic tmp+rename writes.

What changes underneath:

- One bash per Shell becomes one tmux session (containing one bash)
  per Shell.
- The Go process no longer owns the PTY directly. Instead it talks to
  tmux via short-lived `tmux send-keys` invocations and reads bash
  output from a file that `tmux pipe-pane` continuously appends to.
- A new `session` package owns the lifecycle of N Shells, plus the
  reconciliation logic that runs at Go startup to reconcile
  `sessions.json` with `tmux ls`.

## 3. Architecture

```
┌─ Pod (container) ───────────────────────────────────────────┐
│                                                              │
│  tini ──── alfred-server ──── tmux server                    │
│                │                  │                          │
│                │ send-keys        ├── session "01HX..." ──── │
│                │ short-lived      │     ├── bash (PID A)     │
│                │ subprocess       │     └── pipe-pane → file │
│                │                  ├── session "01HY..."      │
│                │ reads file       │     ├── bash (PID B)     │
│                │ offset+continue  │     └── pipe-pane → file │
│                │                  └── ...                    │
│                ↓                                             │
│           /data/                                             │
│             sessions.json                                    │
│             sessions/                                        │
│               01HX.../                                       │
│                 pty.stream    ← tmux writes, Go reads       │
│                 pty.offset    ← Go's last-read position     │
│                 commands/<cmdID>.json                        │
│                 outputs/<cmdID>.log                          │
└──────────────────────────────────────────────────────────────┘
```

### Why `pty.stream` is a regular file, not a FIFO

The natural choice for "pipe bash output to my process" is a named
pipe (FIFO). It was the recommended approach until we walked through
the restart scenario:

> Go-process restart closes the reader end of the FIFO. Bash's
> tmux-spawned writer (`cat > /path/pty.fifo`) gets SIGPIPE on the
> next write and dies. tmux sees the pane's program exit; with
> default settings the session is destroyed.

This violates goal #4 (Go-process restart must not kill bash). The
practical alternative — regular file plus offset bookkeeping — has
none of this. Bash writes to a normal file with no consumer
backpressure, no SIGPIPE, no shared kernel buffer to overflow. The
cost is unbounded file growth, which we deal with by
sentinel-aligned truncation (§4.4).

### Component boundaries

```
api/        HTTP + WS handlers, multi-session routes
 ├── session.Manager   "what sessions exist, get a Shell for one"
 │    └── shell.Shell  per-session bash + sentinel parser
 │         └── tmux subprocess invocations
 └── store.Store       per-session command JSON + output log files
```

The `shell` package keeps the same external surface: `Write`, `Stop`,
`Close`, `SubscribeEvents`, `CurrentCommand`. The implementation
changes from "fork bash + own PTY" to "talk to tmux + read offset
file." `api/` only ever sees a `Shell` value; it doesn't know whether
it's the old PTY-owning implementation or the new tmux-backed one.

A new `session` package owns the cross-Shell lifecycle: create,
list, rename, close, reconcile-at-startup.

## 4. The tmux integration

### 4.1 Server bring-up

The tmux server is started lazily on first need from a single
well-known socket path. `alfred-server` does not run tmux as a child
process — it invokes tmux subcommands, and tmux's own daemonization
puts the server in the background. The server stays alive as long as
at least one session exists. If every session is closed and a new
one is created later, tmux will daemonize again automatically.

Socket: `/data/alfred-tmux.sock` (inside the container).

Why on the data PVC? So a future iteration can mount the socket
across Pod restarts. Not relied on now.

### 4.2 Session creation

```
sessionID := ulid.Make()
mkdir -p /data/sessions/<sessionID>/{commands,outputs}
touch /data/sessions/<sessionID>/pty.stream
echo 0 > /data/sessions/<sessionID>/pty.offset

tmux -S /data/alfred-tmux.sock \
  new-session -d -s <sessionID> \
  bash --noprofile --norc

# Disable PTY echo before any user command — same reason as the
# single-bash design (CONTEXT.md "traps" table).
tmux -S /data/alfred-tmux.sock \
  send-keys -t <sessionID> 'stty -echo' Enter

# Tee the pane's bytes into the regular file.
tmux -S /data/alfred-tmux.sock \
  pipe-pane -t <sessionID> -o \
  'cat >> /data/sessions/<sessionID>/pty.stream'

# Persist metadata.
append-or-update /data/sessions.json with
  { id: sessionID, name: "Session N", created_at: now }

# Spawn the goroutine that reads pty.stream and feeds the sentinel
# parser. Starts at offset stored in pty.offset (0 for fresh session).
go readLoop(sessionID)
```

### 4.3 Command execution

When `Shell.Write(cmdID, cmd)` is called:

```
wrapped := Wrap(nonce, cmdID, cmd)   // existing function
exec.Command("tmux", "-S", socket,
  "send-keys", "-t", sessionID, wrapped).Run()
```

`send-keys` returns immediately. bash inside tmux executes the
wrapper, prints the START sentinel, runs the user command, prints
the END sentinel. Those bytes arrive in `pty.stream` (via
`pipe-pane`). The `readLoop` goroutine reads them, feeds the
sentinel parser, and the parser emits `Started` / `Chunk` / `Ended`
events on the per-Shell broadcaster.

This is the SAME event flow as the old design. The only thing that
changed is the source of bytes: a regular file instead of a PTY
master fd.

### 4.4 `pty.stream` growth and truncation

`pipe-pane` appends forever. Without bounding it, a long-lived
session running `npm install` over and over would accumulate
gigabytes.

Strategy: the parser already knows where one command ends
(EventEnd's byte position in the stream). After each EventEnd we:

1. Persist the current offset to `pty.offset` (atomic tmp+rename).
2. If `pty.stream` size > `STREAM_TRUNCATE_THRESHOLD` (default 8 MB),
   truncate the file from the start up to the just-recorded offset:
   - Open `pty.stream` and a fresh tmp file.
   - Read from current offset to EOF, write to tmp.
   - Rename tmp over `pty.stream`.
   - Reset `pty.offset` to 0.

Truncation only happens when the parser is between commands (no
in-flight `Started` without matching `Ended`), so we never lose
output bytes of a running command.

**Concurrency concern:** tmux's `pipe-pane` writer is still open on
the file when we truncate. A naive `rename(tmp, pty.stream)` swaps
the inode; tmux's open fd keeps pointing at the now-unlinked inode
and silently writes into a freed file — output is lost.

Therefore we **stop the pipe, truncate in place, restart the pipe**:

```
tmux pipe-pane -t <sessionID>           # stops the pipe (no command)
truncate /data/sessions/<sessionID>/pty.stream  to bytes [offset:]
echo 0 > /data/sessions/<sessionID>/pty.offset
tmux pipe-pane -t <sessionID> -o 'cat >> /data/.../pty.stream'  # resume
```

This is racy in principle (output produced between stop and resume
is lost). We minimize the window by only doing it when the parser is
idle for that session, and we accept that during the ~50ms truncation
window any bytes bash happens to emit are lost. Acceptable for a
single-user tool. If this proves problematic in practice, we
fallback to "let `pty.stream` grow, GC sessions on close."

### 4.5 Stopping a command

`Shell.Stop()` SIGKILLs the bash inside the tmux session.

```
tmux -S <sock> kill-session -t <sessionID>   would kill the session;
                                              we don't want that.
```

Instead we get the bash PID via tmux and SIGKILL it directly:

```
pid := exec("tmux", "list-panes", "-t", sessionID,
            "-F", "#{pane_pid}").Output()
syscall.Kill(pid, SIGKILL)
```

tmux notices the pane's program died and — with the session's
`remain-on-exit` set to `off` (default) — kills the session too.
**For Stop we want the session to survive** so we set:

```
tmux set-option -t <sessionID> remain-on-exit on
```

on each session at creation. After the kill, tmux marks the pane
"dead" but keeps the session. We then `respawn-pane` with bash to get
a fresh shell:

```
tmux respawn-pane -k -t <sessionID> bash --noprofile --norc
tmux send-keys -t <sessionID> 'stty -echo' Enter
```

The trade-off — cwd / env / aliases reset on Stop — is the same as
the single-bash design (CONTEXT.md). User-visible behavior is
unchanged.

### 4.6 User-initiated `exit` inside bash

If the user types `exit` (or Ctrl-D) at the bash prompt, bash exits
voluntarily. With `remain-on-exit on` the pane becomes dead but the
tmux session lives. We treat **voluntary bash exit as "user is done
with this session"**:

- A per-session poller (1 Hz, cheap) runs `tmux display-message
  -t <id> -p '#{pane_dead}'`. When it returns `1` the pane is dead.
- The poller checks an in-memory flag `stoppingForRespawn` first:
  - If set → this is a Stop in progress, the Stop handler will call
    `respawn-pane`. Poller does nothing.
  - If not set → bash exited on its own. Close the session: `tmux
    kill-session`, `rm -rf` the session directory, remove from
    `sessions.json`, push `session_closed` to all WS clients.
- The `stoppingForRespawn` flag is set by `Stop()` immediately
  before SIGKILL and cleared after `respawn-pane` succeeds.

This matches terminal-app intuition (`exit` in iTerm closes the tab)
without mistaking a Stop-then-respawn cycle for a user `exit`.

### 4.7 Reconciliation at startup

```
On alfred-server boot, before opening the HTTP listener:

stored  := readSessionsJSON()         // sessions we know about
live    := exec("tmux ls").parse()    // sessions tmux has now

for each id in stored ∩ live:
    // Normal Go-restart case. tmux is still running, bash is still
    // alive, sentinel parser will resume from pty.offset.
    start readLoop(id) at offset = readPtyOffset(id)

for each id in stored \ live:
    // Pod restart, or tmux died. The bash from before is gone; the
    // command history is still on disk. Re-create the tmux session
    // (new bash) so the user can keep working.
    tmux new-session -d -s <id> bash --noprofile --norc
    set-option remain-on-exit on
    send-keys 'stty -echo' Enter
    pipe-pane -o 'cat >> /data/sessions/<id>/pty.stream'
    truncate /data/sessions/<id>/pty.stream to 0
    reset pty.offset to 0
    start readLoop(id) at offset 0

    // Mark any "running" command in this session as interrupted —
    // the bash that was running it is gone.
    for each cmd JSON in /data/sessions/<id>/commands/ where status==running:
        update status to "interrupted", set finished_at = now

for each id in live \ stored:
    // Unexpected: tmux has a session we don't know about. Log and
    // tear it down; don't trust state we didn't write.
    tmux kill-session -t <id>

Only after reconciliation completes do we open the HTTP listener.
```

This is the heart of the goal-4 implementation. The diff in pty.offset
between two startups tells us "Go was away from this much output";
when we resume reading, sentinel parser naturally re-emits any
`Started` events for commands that completed during the gap, and
treats commands still in-flight (START seen, no END) as currently
running (which they are — bash is still running them).

## 5. Storage layout

```
/data/
  alfred-tmux.sock                ← tmux server socket
  sessions.json                   ← session metadata (atomic write)
  sessions/
    <sessionID>/
      pty.stream                  ← tmux pipe-pane appends; Go reads
      pty.offset                  ← last byte Go's parser has consumed
      commands/<cmdID>.json       ← per-command metadata (existing schema, + session_id field)
      outputs/<cmdID>.log         ← per-command output (existing)
```

`sessions.json`:

```json
[
  {
    "id": "01HX...",
    "name": "training",
    "created_at": "2026-06-11T13:00:00Z"
  },
  ...
]
```

### Schema migration

Existing `/data/commands/*.json` + `/data/outputs/*.log` from the
single-bash era is migrated on first boot:

1. If `/data/sessions.json` doesn't exist AND `/data/commands/`
   exists → migration mode.
2. Create a session with name "Imported" and a new ULID.
3. `mv /data/commands/* /data/sessions/<imported>/commands/`.
4. `mv /data/outputs/* /data/sessions/<imported>/outputs/`.
5. For every migrated `*.json`, add `"session_id": "<imported>"`.
6. Write `sessions.json` with that one entry.
7. Remove the now-empty `/data/commands` and `/data/outputs`.

Migration runs before reconciliation. After it, fresh containers
start with zero sessions; reconciliation will see zero stored, zero
live, and the API will return an empty list until the user clicks
"+ New chat".

## 6. API changes

### 6.1 New REST endpoints

| Method | Path | Body / Response |
|---|---|---|
| `GET` | `/api/sessions` | `[{id, name, created_at}]` |
| `POST` | `/api/sessions` | `{name?}` → `{id, name, created_at}`; 422 `session_limit` if >= 8 |
| `PATCH` | `/api/sessions/{id}` | `{name}` → 200; 422 if name is empty |
| `DELETE` | `/api/sessions/{id}` | 204; idempotent (404→404 only if id was never seen) |
| `GET` | `/api/sessions/{id}/commands` | as today's `/api/commands` |
| `GET` | `/api/sessions/{id}/commands/{cmdID}` | as today |
| `POST` | `/api/sessions/{id}/commands/{cmdID}/stop` | as today |

Top-level `/api/commands*` from the single-bash era are removed.

Auth: existing Bearer token, same middleware.

Rename name validation:

- Empty or all-whitespace → 422 `bad_name`.
- Duplicates allowed (single-user tool; user takes responsibility).
- Max length 64 chars; trim leading/trailing whitespace.

### 6.2 WS protocol changes

The existing message kinds gain a `sessionID` field. The complete
new set:

**Client → server:**
```typescript
{ type: 'run', sessionID: string, command: string }
{ type: 'ping' }
```

**Server → client:**
```typescript
{ type: 'idle', sessionID: string }
{ type: 'reattach', sessionID: string, cmdId, command, startedAt, outputSoFar }
{ type: 'started', sessionID: string, cmdId, command, startedAt }
{ type: 'chunk', sessionID: string, cmdId, data }
{ type: 'done', sessionID: string, cmdId, exitCode, finishedAt }
{ type: 'error', sessionID?: string, code, message }
//   sessionID is set when the error is scoped to a specific session
//   (e.g., "busy" on a Run). It is absent for connection-wide errors
//   (e.g., "rate_limited", "tmux server unreachable").
{ type: 'session_closed', sessionID: string }   // NEW
{ type: 'session_renamed', sessionID: string, name: string }   // NEW
{ type: 'pong' }
```

On WS open the server immediately emits, for every session known
to the manager, either an `idle` or `reattach` event scoped to
that session. The client uses these to seed per-session state.

`session_closed` lets tabs that were viewing a session deleted from
another tab fall back gracefully (see §7.4).

`session_renamed` lets the sidebar in other tabs reflect a rename
without polling.

### 6.3 Backwards compatibility

There is no API backwards compatibility. This is a single-user tool
and the upgrade story is: deploy the new image, the migration step
in §5 imports old commands into a session named "Imported", users
see all their old history. Frontend and backend deploy atomically.

## 7. Frontend changes

### 7.1 Layout

```
┌─ Sidebar (260px) ────────────┬─ Chat stream (existing) ──┐
│ + New chat                    │                            │
│                               │   selected session's       │
│ ACTIVE SESSIONS               │   command + output         │
│  • Session 1                  │   turns, ChatGPT-style     │
│  • training        ←selected  │                            │
│  • db-debug                                                 │
│                                                             │
│  hover: ×        rename                                     │
│                                                             │
│                               │  [ Type a command… ↑ ]     │
└───────────────────────────────┴────────────────────────────┘
```

- Logo and "Sign out" stay in the top-right of the chat pane; the
  sidebar handles session listing only.
- Connection status dot lives next to the logo (existing).
- Sidebar header: "ACTIVE SESSIONS" (uppercase label, faint).
- Each row: name + (hover) trailing × button. Double-click name to
  rename in-place via an inline `<input>`. Enter / blur to commit,
  Esc to cancel.
- "+ New chat" button: disabled when sessions.length >= 8 with
  tooltip "Close one first".
- Selected row: subtle grey background (matches the ChatGPT
  reference screenshot the user shared).

### 7.2 State model

`useShell(token)` is replaced by `useSessions(token)`. It owns:

```typescript
{
  connState: 'connecting' | 'open' | 'reconnecting' | 'closed'
  sessions: Session[]                     // metadata only (from REST)
  selectedSessionID: string | null        // persisted to localStorage
  perSession: Map<string, PerSessionState>
  lastError: ErrorMessage | null
  // … callbacks …
}

type PerSessionState = {
  running: RunningCmd | null
  messages: CompletedMsg[]                // hydrated lazily on first selection
  messagesLoaded: boolean
}
```

WS chunk events update `perSession.get(sessionID).running.output`
regardless of which session is currently selected. The user can
switch to Session A, kick off `sleep 60`, switch to Session B, run
`ls`, switch back to A, see the sleep still running and the output
continuing to stream.

Memory cap: each `PerSessionState.messages` is capped at the same
50-entry initial load. Older messages can be re-fetched on demand
if you scroll up far (not implemented in this iteration — out of
scope).

### 7.3 Selection persistence

`selectedSessionID` lives in `localStorage`. On mount:

1. Fetch session list.
2. If `selectedSessionID` is in the list, use it.
3. Otherwise pick the first session (or `null` if empty).

The session list itself is **not** in localStorage; it's always
fetched fresh.

### 7.4 Cross-tab consistency

Two tabs open at once. Tab B deletes Session 5; Tab A was viewing it.

Server broadcasts `{ type: 'session_closed', sessionID: '01HX…' }`
to every connected WS client (including Tab A). On receipt:

```
remove from sessions[]
delete perSession.get(sessionID)
if selectedSessionID == that one:
    selectedSessionID = sessions[0]?.id ?? null
    localStorage.update
```

Same pattern for `session_renamed`: update `sessions[i].name`.

### 7.5 The "no sessions yet" state

When `sessions.length == 0`:

- Chat pane shows: large "Headless Alfred" heading + "Create a
  session to begin." + a "+ New chat" button.
- Sidebar shows just the "+ New chat" button; no list.

After clicking, the new session is auto-selected.

### 7.6 Confirmation before destructive actions

- Closing a session with at least one running command:
  > "Close 'training'? 1 command is still running and will be
  > terminated. The session and 47 commands of history will be
  > permanently deleted."
- Closing a session with no running command:
  > "Close 'training'? The session and 47 commands of history will
  > be permanently deleted."

Both have a "Cancel" / "Delete" pair. Delete is the danger color.

### 7.7 The composer

Same composer as today (rounded pill, send/stop). Draft text is
**per-tab** and **not** preserved across session switches: switching
from A to B with text in the box discards it. (Trivial to revisit
later; not needed for the first cut.)

## 8. Error handling

### 8.1 tmux subprocess failures

Every `exec.Command("tmux", ...).Run()` can fail. Failure modes:

- **tmux binary not on PATH:** alfred-server exits 1 at boot with a
  clear error. Dockerfile must install tmux (Debian package). Add it
  to `Dockerfile`.
- **tmux server died:** the next `send-keys` / `list-panes` returns
  "no server running on /data/alfred-tmux.sock". Manager catches it,
  logs ERROR, falls back to reconciliation: relaunch tmux, recreate
  every session from `sessions.json` (fresh bashes, in-flight commands
  marked interrupted). User sees a banner: "Backend recovered from
  tmux crash; in-flight commands were interrupted."
- **session-specific failure (e.g., kill-session on a non-existent
  session):** treat as already-done; idempotent.

### 8.2 Disk full

`pty.stream` truncation strategy keeps files bounded. Per-command
JSON + output files are bounded by `MaxBufferBytes = 10MB` each.
A reasonable cap on total disk: 8 sessions × 8 MB pty.stream + 8 ×
maybe 100 commands × 10 MB ≈ 8 GB. Document this in the deploy
README; the PVC is 1 GB by default (existing).

If atomic JSON writes fail with ENOSPC the command record is dropped
and the UI sees no `done` event for that command — same failure mode
as the single-bash design.

### 8.3 Race between Close and an in-flight command

User clicks × on a session while it's running `npm install`.

1. Confirmation dialog shows "1 command is still running" (frontend
   queries perSession state, no extra call).
2. User confirms.
3. `DELETE /api/sessions/{id}` arrives.
4. Manager:
   - kill the bash inside the session (SIGKILL pane_pid).
   - **wait for** sentinel parser to emit the synthetic Ended event
     for the running command (exit code -1, marked interrupted) —
     this writes the final command JSON.
   - tmux kill-session.
   - rm -rf the session directory.
   - Remove from sessions.json.
   - Broadcast session_closed.
5. Response 204.

The "wait for synthetic Ended" step bounds the time the DELETE call
takes; in practice <100ms. If the readLoop is stuck (shouldn't
happen), we time out after 2s and proceed with the kill anyway —
the to-be-deleted command JSON gets force-marked interrupted by the
Close path.

### 8.4 Reconcile fails

If `tmux new-session` fails during reconciliation for a stored
session (e.g., tmux is fundamentally broken), we mark the session
as `unreachable` in memory and serve a banner in the UI. The user
can still view its command history (read-only) and Close it.

This is an unlikely failure mode but cheap to guard against.

## 9. Testing strategy

Test investment in this feature is high (~30-40% of code volume) on
purpose. The bug pattern this codebase already documents — five
sentinel-parser traps caught only by regression tests (CONTEXT.md)
— recurs whenever async + process + file + WS layers compose.
tmux adds an external subprocess to that pile.

Existing tests stay green:

- `internal/shell/sentinel_*` — unchanged; the parser is unmodified.
- `internal/store/*` — unchanged; the per-session directory layout
  is transparently abstracted.
- `web/src/features/auth/useAuth.test.ts` — unchanged.

### Backend unit tests

- `internal/session/manager_test.go`:
  - Create / List / Rename / Close happy paths.
  - Session limit enforced at 8.
  - Rename: empty name → 422; trim whitespace; max length 64.
  - Reconcile, all three branches:
    - stored ∩ live → resume at stored `pty.offset`.
    - stored \ live → rebuild tmux session, mark running commands
      interrupted, old command JSONs still listable.
    - live \ stored → kill orphan tmux session.
  - Migration: seed a tmpdir with `/data/commands/*.json` +
    `/data/outputs/*.log`, run `Manager.MigrateIfNeeded()`, assert
    "Imported" session exists, files moved, old dirs removed.

- `internal/shell/tmux_shell_test.go`:
  - Mock the tmux subprocess (interface tmux invocations behind a
    `TmuxRunner` interface for the test).
  - Send a command via Write, fake bytes into `pty.stream`, assert
    Started / Chunk / Ended events fire.
  - `pty.stream` truncation when threshold crossed: write 9 MB, end
    command, write 9 MB, end command — assert no bytes lost from
    either command's persisted output, `pty.offset` resets correctly.
  - `stoppingForRespawn` flag suppresses the dead-pane auto-close.

- `internal/api/sessions_handler_test.go`:
  - REST endpoints for sessions CRUD.
  - WS multi-session demultiplexing: one connection, two simulated
    sessions, assert chunks land in the correct session bucket.
  - `session_closed` and `session_renamed` broadcast to all
    connected WS clients.

### Frontend unit tests

- `web/src/features/sessions/useSessions.test.ts`:
  - Multiple per-session running states maintained independently
    (chunks for session A don't disturb session B's state).
  - `selectedSessionID` rehydrates from localStorage; falls back to
    first session if stored id no longer exists; null if list empty.
  - `session_closed` event removes session from list, clears it
    from `perSession`, falls back selection.
  - `session_renamed` updates `sessions[i].name` without disturbing
    `perSession`.
  - Switching session preserves the previous session's running
    state in memory (chunks continue accumulating in background).

### E2E (kind cluster)

This is the highest-leverage layer for this feature. The list below
is grouped by "must pass to ship" vs "recommended but can land in a
follow-up commit". `test/e2e/` is the file these live in.

#### Must pass to ship

- **`TestE2E_TwoSessions_FilesystemShared`**: create two sessions,
  in one `mkdir /tmp/foo`, in the other `ls /tmp/foo` exits 0.
- **`TestE2E_GoRestart_SessionsSurvive`**: create a session, kick
  off `sleep 10 && echo done`, restart the alfred-server process
  inside the Pod (so tmux survives). After restart, the session
  is still listed, the command finishes, and the chat stream shows
  the full output.
- **`TestE2E_Reconcile_StoredButNotLive_RebuildsSession`**: simulate
  the Pod-restart branch — kill tmux server (so live={}, stored
  remains) and let alfred-server reconcile. Existing sessions get
  rebuilt with fresh bashes; any running commands' JSONs are
  marked interrupted; old completed-command JSONs are still
  listable in the API.
- **`TestE2E_GoRestart_DuringStreamingChunks`**: kick off `for i in
  $(seq 100); do echo $i; sleep 0.05; done`, kill alfred-server
  around chunk 50, restart. Verify (a) the persisted output ends
  with `100\n` (no missing or duplicated numbers), (b) the command
  is marked completed (not interrupted), (c) `pty.offset` matches
  the byte after the END sentinel.
- **`TestE2E_PtyStream_Truncation_NoLostBytes`**: in one session
  run three commands in sequence: `yes | head -c $((6*1024*1024))`,
  then `pwd`, then `yes | head -c $((6*1024*1024))`. Truncation
  happens between commands 1 and 3. Verify all three commands'
  `outputs/<cmdID>.log` are byte-exact (6 MB + a few bytes + 6 MB).
- **`TestE2E_EightConcurrentSleeps_NoSerialization`**: create 8
  sessions, kick off `sleep 5` in all 8 simultaneously, all
  complete within 6 seconds wall-clock. A serialized implementation
  takes 40 s; this is our cheap concurrency regression test.
- **`TestE2E_CrossSession_NoOutputBleed`**: in two sessions
  concurrently run `for i in $(seq 1 1000); do echo SECRET_A; done`
  (resp. SECRET_B). Verify session A's `outputs/*.log` contains 1000
  SECRET_A and zero SECRET_B (and vice versa).
- **`TestE2E_CloseSession_RunningCommandTerminated`**: kick off
  `sleep 30`, DELETE the session, verify the bash PID is gone, the
  session directory is removed, and the in-flight command JSON is
  written as interrupted (not left half-done).
- **`TestE2E_SessionLimit`**: create 8 sessions, attempt 9th → 422
  with `code: "session_limit"`.

#### Recommended (land soon, not blocking ship)

- **`TestE2E_Reconcile_LiveButNotStored_KillsOrphan`**: manually
  `tmux new-session -d -s ghost` directly on the alfred-server's
  socket (or simulate via the test harness), start reconcile, verify
  the ghost session is gone after reconcile.
- **`TestE2E_BashExit_AutoClosesSession`**: in Session 2, run
  `exit`. Within 2 seconds (one poller tick + slack): session is
  removed from API list, WS `session_closed` was broadcast, the
  session directory is removed. **Crucially**, Session 1 is
  untouched (regression guard for poller-cross-session bugs).
- **`TestE2E_Stop_RestartsBashSameSession`**: kick off `sleep 60`,
  POST stop. Verify (a) the command exits non-zero, (b) the bash
  PID inside the tmux session has changed (respawn-pane fired),
  (c) the session is still listed, (d) the next `pwd` works.
- **`TestE2E_Migration_OldSchemaImported`**: pre-seed `/data/
  commands/*.json` + `/data/outputs/*.log` representing the old
  single-bash layout, then boot. After boot the API lists exactly
  one session named "Imported"; its commands include every
  pre-seeded JSON; `/data/commands` and `/data/outputs` are gone.
- **`TestE2E_RenamePersistsAcrossReload`**: create a session, rename
  it to "training", restart alfred-server, GET /api/sessions, name
  is still "training".

#### Existing E2E reuse

Of the seven E2E scenarios from the single-bash era, three carry
real value in the multi-session world and are minimally adapted
(create a default session at test setup, target it explicitly in
the WS run message):

- `TestE2E_RunSimpleCommand` (smoke: round-trip a `pwd` end-to-end)
- `TestE2E_DisconnectReconnect_PicksUpRunningCommand` (reattach
  protocol)
- `TestE2E_StopRunningCommand` (already covered more tightly by
  `Stop_RestartsBashSameSession`, but cheap to keep)

The other four (`RunSlowCommand_StreamingOutput`,
`CDPersistsAcrossCommands`, `NoToken_WSRejected`,
`WrongPassword_Rejected`) are subsumed by the new scenarios above
or by existing unit tests; they're deleted, not "renamed to
single-session".

## 10. Open questions deferred to writing-plans

- Exact UI layout of the rename input (inline vs popover): the spec
  says "inline", the plan will decide CSS specifics.
- Whether to ship a small "tmux died, here's why" diagnostic
  endpoint at `/api/_debug/tmux` — useful for ops, but personal-tool
  scope.
- How to test the truncation race between tmux pipe-pane re-attach
  and bash output: pure unit-test with a fake `Runner` should
  suffice; if not, an integration test in a real shell environment.

## 11. Out of scope (explicitly)

- **Resize / window dimensions.** `tmux` panes have a size; we'll
  set it to a sensible default (200×50) once at session creation
  and never touch it. Long lines wrap in the chat-stream UI anyway.
- **Multiple windows / panes per session.** One bash per session.
- **Session sharing across users.** Single-user tool.
- **An "archive" of closed sessions you can rehydrate.** Close
  means delete.
- **A search box over sessions.** Up to 8 sessions; not needed.
- **Live cursor / typing indicators across tabs.** Each tab is
  independent.

## 12. Acceptance criteria

- User can create up to 8 sessions, each isolated bash state.
- Files created in one session are visible in another (same fs).
- Renaming a session persists across page reload and is visible in
  another tab without manual refresh (`session_renamed` push).
- Closing a session removes it from all tabs (`session_closed` push)
  and frees disk.
- Restarting the Go process (kubectl rollout, local `kill`+`go run`)
  while a command is mid-stream:
  - bash continues running through the restart.
  - The command's output continues accumulating in pty.stream.
  - On Go boot, the parser resumes from `pty.offset`, re-emits
    `Started` if mid-command, emits `Ended` when the command
    finishes, and the user sees the complete output in the
    re-rendered chat stream.
- Restarting the Pod (i.e., the container) does NOT preserve bash
  state. Past commands' JSON history is still on disk and shown in
  the chat stream; any in-flight command is marked interrupted.
- Running 8 concurrent `sleep 30` across 8 sessions: all 8 finish at
  the same wall-clock time ± a few hundred ms (proves no
  serialization).
