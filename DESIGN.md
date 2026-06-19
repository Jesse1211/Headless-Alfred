# UI Background Tasks Panel — DESIGN.md

**Date:** 2026-06-19
**Status:** Approved (feature-factory Phase 1 complete; ready for hands-off build)
**Authoring branch:** `refactor/refresh-parity`
**Target branches:** PR-A → PR-B → PR-C → PR-D (see ADR-021)
**Supersedes:** `docs/superpowers/specs/2026-06-19-ui-mode-background-tasks-design.md`

## Problem

In UI mode (the React-rendered Markdown chat), Claude CLI can spawn
background bashes via `Bash(run_in_background=true)` or the `Monitor`
tool. Today:

- **Frontend has no global visibility into them.** They render only
  inline per-turn, gated on `turn.done`, hardcoded to `tool.name ===
  'Monitor'`. `Bash(run_in_background)` is invisible mid-turn.
- **Subagents (`Task(subagent_type=...)`) collide semantically** with
  background tasks in the UI's current "stats line" wording, even
  though Subagents are *synchronous* — main Claude blocks on them.
- **Server is blind to bg-task output.** The CLI writes each task's
  stdout to a file, but alfred-server never reads it.

This design adds a header-anchored Background Tasks panel that is
visible at all times in UI mode, lists active and recently-finished
background tasks AND subagents (with distinct grouping), and supports
"View logs" by tailing the file the CLI already writes. Refresh parity
holds: panel state survives page reload via the existing
`/claude-state` hydrate path; bg-task lifecycle metadata is rebuilt
from the per-session `.jsonl` on alfred-server restart.

## Non-goals

- Extending bg-task lifetime past the CLI's. **Spike confirmed CLI
  actively SIGKILLs its in-flight bg tasks at `claude -p` exit, by
  design.** We display this honestly; we do not work around it.
- Adding a Stop button. The CLI does not expose a way to stop
  individual bg tasks externally; the only mechanism is
  process-level signaling, which conflicts with the CLI's own kill
  path. Out of scope.
- TUI mode background visibility. The 1-shell / `↓ to manage` UI in
  TUI mode is rendered inside Claude's terminal output bytes; alfred
  cannot parse it. Users wanting bg-task observability switch to UI
  mode.
- Cross-pod-restart durability. `kubectl rollout` kills the alfred
  pod which kills bg tasks (CLI's behavior + we do not foster them).
- A general task manager (queue / priority / archive). The panel is
  live observability for the current and recently-finished tasks
  only; persistence is bounded by the in-memory ClaudeState + a
  60-second "recently finished" window on the frontend.

---

## Architecture decisions (ADR ledger)

Each ADR is a **locked** decision the build agents must implement
verbatim. Anything not in this ledger is by convention either trivial
(follow codebase style) or covered by an OQ.

### ADR-001 — Observer-only design

UI mode background-task support is an **observer** pattern, not a
takeover. alfred-server watches what the CLI does (via the existing
`task_started` / `task_notification` / `task_updated` stream-json
events plus the CLI's output files), and never tries to own bg-task
processes. **Rationale:** Spike on 2026-06-19 with Claude CLI 2.1.169
confirmed the CLI parents every `Bash(run_in_background=true)` as its
own child (not detached via setsid) and SIGKILLs all in-progress
tasks when `claude -p` exits, emitting
`task_updated.patch.status="killed"`. The detached-bash + tmux
ownership model was impossible.

### ADR-002 — `runner.go` and `exec.Command` path unchanged

UI mode keeps spawning `claude -p` via `exec.Command` inside
alfred-server (current `internal/claude/runner.go`). No tmux
containerization, no `internal/claudeuiwindow/` new package.
**Rationale:** Trying to put `claude -p` inside a tmux window does
not help any of the three traps in the old spec (CLI kills tasks
regardless of who started it). Less code is better.

### ADR-003 — No tmux containerization for UI mode

Cancels the old spec's `claudeuiwindow` package and the two-window-
per-session model. **Rationale:** Same as ADR-002.

### ADR-004 — No `ALFRED_LEGACY_CLAUDE_RUNNER` env hatch

Old spec planned an env hatch to roll back to a "legacy runner". With
ADR-002 there is no migration, so no hatch needed. The current runner
is the only runner. **Rationale:** YAGNI.

### ADR-005 — No Stop button, no `KillBgTask`

The panel is read-only with respect to bg-task lifecycle. **Rationale:**
CLI is the lifecycle owner; injecting kills bypasses its bookkeeping
and would leave its internal state inconsistent (the CLI would still
think the task is in_progress and might re-emit events).

### ADR-006 — BgTask output file path rule

Path: `<CLAUDE_CODE_TMPDIR>/claude-<uid>/<cwd-mangled>/<session-uuid>/tasks/<task-id>.output`

Where:
- `<CLAUDE_CODE_TMPDIR>` — env var; CLI defaults to literal `/tmp`
  if unset (NOT `os.TempDir()`, NOT `$TMPDIR`).
- `<uid>` — `os.Getuid()` decimal, no zero-padding.
- `<cwd-mangled>` — `originalCwd` is first NFC-normalised then
  `realpath`-resolved at CLI session start. Then every non-
  `[a-zA-Z0-9]` char is replaced with `-`. If the result is > 200
  chars, truncate to 200 + `-` + base-36 of
  `Math.abs(javaHash(originalCwd))` where
  `javaHash = ((h<<5)-h+ch)|0` reducing over UTF-16 code units.
- `<session-uuid>` — Claude's session UUID (we already track it as
  `SessionMeta.ClaudeSessionID`).
- `<task-id>` — opaque string from `task_started.task_id`. Validator
  regex `/^[a-zA-Z0-9_-]{1,20}$/`; the codebase **does not** pin a
  tighter shape (see OQ-02).

**Rationale:** Reverse-engineered from CLI 2.1.169 Bun bundle on
macOS. Predicted to hold identically on Linux pods (CLI sources tmp
root from env or hard-coded `/tmp`; no `os.tmpdir()` path).

### ADR-007 — alfred-server bootstraps `CLAUDE_CODE_TMPDIR` and `ALFRED_CLAUDE_BG_TASK_DIR`

At startup, alfred-server computes `bgTmpRoot := <dataDir>/claude-tmp`,
then atomically:

1. `os.Setenv("CLAUDE_CODE_TMPDIR", bgTmpRoot)` — so CLI writes
   under our PVC, not `/tmp` (which on a node-shared `/tmp` could
   collide with other pods of the same uid).
2. Computes the default for `ALFRED_CLAUDE_BG_TASK_DIR` =
   `bgTmpRoot + "/claude-" + strconv.Itoa(os.Getuid())`. If the user
   has set `ALFRED_CLAUDE_BG_TASK_DIR` explicitly in env, that
   override wins.

The two values are **bound at startup**; the user cannot drift them.
This is the only correct place to set both — they must point at the
same `claude-<uid>` directory or path resolution fails. **Rationale:**
Spike found node-shared `/tmp` is a real collision risk; one env var
is the canonical workaround.

### ADR-008 — `cwd` passed to `runner.Prompt` is realpath-resolved

`internal/api/claude_handlers.go::handleClaudePrompt` already calls
`filepath.EvalSymlinks` on the cwd before passing to runner — this
is **load-bearing** because the CLI realpath-resolves its own
`process.cwd()` before mangling, so an unresolved cwd given to
runner would produce a different `<cwd-mangled>` than the one CLI
writes the file under. **Rationale:** Spike + existing code; this
ADR records the dependency so a future refactor does not break it.

### ADR-009 — Subagents map is NOT rebuilt on alfred-server restart

`claudestate.Loader.Load` leaves `Subagents` as an empty map after
restart, even if the .jsonl contains `SubagentStart` / `SubagentStop`
events. **Rationale:** Subagents require a live main Claude per CLI
contract (Task tool is synchronous); after alfred-server restart, the
hosting `claude -p` is dead, so subagents are dead too. Showing
historical subagents would imply they are running.

### ADR-010 — Server BgTask struct shape unchanged; status enum widened

`internal/claudestate/types.go::BgTask` keeps its existing field set
(no new persisted fields). The `Status` field is a Go string but its
domain widens to: `"in_progress" | "completed" | "failed" | "killed"
| "stopped"`. The TS mirror `BgTask['status']` widens identically.
**Rationale:** Spike showed CLI emits `killed` (turn-end SIGKILL) and
`stopped` (natural bg-task exit) in addition to the three we already
handle.

### ADR-011 — No SIGHUP / updatedInput / pipe-pane spikes

The three pre-implementation spikes from the old spec are cancelled
because the architecture they shaped (tmux containerization +
bridge-rewritten commands + pipe-pane demux) is cancelled by
ADR-001. **Rationale:** Spike on 2026-06-19 made them moot.

### ADR-012 — No bridge `tool_input.command` rewriting

The dispatcher does NOT alter Bash commands. Old spec's "preferred"
log-capture path (rewriting `command` via the PreToolUse hook's
`updatedInput`) is dropped. **Rationale:** CLI writes the bg-task
output to a known path on its own. We just read that path.

### ADR-013 — `ALFRED_CLAUDE_BG_TASK_DIR` env override; no fallback chain

The path resolver uses `ALFRED_CLAUDE_BG_TASK_DIR` as its base
(default from ADR-007). It does NOT try multiple paths in a chain;
if the file is not found at the computed path, the API returns
`{ status: "log_unavailable" }` and the user can override the env to
point at the right place (or we hotfix the resolver and redeploy).
**Rationale:** YAGNI; fallback chains accumulate cruft and hide bugs.

### ADR-014 — fsnotify-per-opened-panel for log streaming

When a user opens "View logs" on a bg task, the frontend sends a WS
`subscribe_bg_task_log { taskId }` frame. Server starts a goroutine
that opens an `fsnotify` watcher on the task's output file, and on
each Write event reads the new bytes and broadcasts a
`bg_task_stdout_chunk { taskId, bytes }` frame. When the user closes
View logs, switches task, the WS disconnects, or the bg task ends,
the watcher is closed and the goroutine returns. **Rationale:**
Reuses `internal/diskwatcher/` infrastructure; on-demand cost (zero
idle); per-WS lifecycle is unambiguous.

### ADR-015 — REST tail + WS incremental for View logs

Initial log load (page mount, refresh, panel reopen) goes through
`GET /api/sessions/{sid}/bg-tasks/{taskId}/log?tail=N` (default
N=8192, max N=65536). The endpoint reads the file's last N bytes and
returns them. After mounting, the panel sends `subscribe_bg_task_log`
to get the WS incremental stream (ADR-014) for further appends.
**Rationale:** Same split as shell mode's
`/api/.../commands/{id}/output` + sentinel-driven WS chunks. Refresh
parity: F5 → REST one-shot → WS subscribe → no gap.

### ADR-016 — 5-value status enum, UI maps killed/stopped distinctly

Frontend `BgTask['status']`: `'in_progress' | 'completed' | 'failed'
| 'killed' | 'stopped'`. UI rendering:

| Status | Visual | Sub-label |
|---|---|---|
| `in_progress` | spinner / colored chip | shows elapsed |
| `completed` | green ✓ | optional `lastEventSummary` |
| `stopped` | green ✓ | optional `lastEventSummary` |
| `killed` | gray chip | "(ended with main Claude)" |
| `failed` | red chip | optional `lastEventSummary` |

`stopped` and `completed` are visually identical (both are "task
ended cleanly"); the distinction is preserved in data only for
diagnosis. `killed` is gray (not red) — it's an expected,
documentable end state, not a failure.

### ADR-017 — Global localStorage key for panel open

`localStorage['alfred_bg_tasks_panel_open']` is a boolean (default
false). Same pattern as `alfred_right_sidebar_hidden` and
`alfred_left_sidebar_collapsed`. **Rationale:** Per-session keys
leak; user wants "panel open" to persist across session switches.

### ADR-018 — BgTasks rebuild on Load: jsonl replay → killed

`claudestate.Loader.Load`, after merging the jsonl + snapshot per
the refresh-parity spec, also replays every `task_started` /
`task_notification` / `task_updated` event in the jsonl and writes
the resulting BgTasks into the in-memory state. For any task whose
final replayed status is `in_progress` (i.e., it was running when
the server died), force the status to `killed` and set
`lastEventSummary = "killed when server restarted"`. **Rationale:**
Without rebuild, chat shows Bash tool blocks with `bgTaskId` linking
to nothing, breaking the chat ↔ panel correspondence. With original
status preserved, the panel would lie ("in_progress" for a dead
task). `killed` is the truthful state.

### ADR-019 — 60-second "Recently finished" fade

Tasks with status ≠ `in_progress` are visible in the "Recently
finished" group for 60 seconds after their `finishedAt`. The check
is `Date.now() - Date.parse(finishedAt) < 60_000` at render time
(no `setTimeout`; refresh-safe). After 60 seconds, they disappear
from the panel (but stay in `BgTasks` map until the next session
event purges them; or until next server restart). **Rationale:**
60s gives enough time to glance at a freshly-finished task; longer
turns the panel into a history scroller.

### ADR-020 — WS frame names

Three new frames:

| Direction | Name | Payload |
|---|---|---|
| outbound | `bg_task_stdout_chunk` | `{ taskId: string, bytes: string }` (base64-encoded raw bytes; frontend decodes for display) |
| inbound | `subscribe_bg_task_log` | `{ taskId: string }` |
| inbound | `unsubscribe_bg_task_log` | `{ taskId: string }` |

snake_case to match `tool_decision_applied` /
`claude_run_ended` / `task_started` style. `bytes` is base64 because
the file content can include arbitrary bytes including null /
control chars, and JSON requires UTF-8 strings.

### ADR-021 — 4 PRs, sequential merge

PRs structured as:

- **PR-A** — Frontend refactor (asTaskPayload merge + types doc +
  BG_TASK_TOOL_NAMES). 0 backend changes. Zero risk; ships
  behavior-unchanged.
- **PR-B** — Backend: env bootstrap, path resolver package, status
  enum widening, REST log endpoint, WS subscribe + fsnotify, Loader
  rebuild.
- **PR-C** — Frontend: reducer for new statuses + chunk frame,
  BackgroundTasksPanel component, header badge, TurnStatsLine
  un-hardcode.
- **PR-D** — CONTEXT.md trap entries + Why-these-choices row +
  BgTask Go doc.

Merge order: A → B → C → D. PR-C tests need PR-B's WS frames to be
real, so PR-B must merge first. PR-D is documentation-only and
serializes after PR-C.

T17 (Playwright E2E `background-tasks-refresh.spec.ts`) is NOT in
the build DAG; it is a **post-build manual verification step** the
human runs locally before merging PR-D. See "Post-build manual
verification" below.

---

## Open questions

`OQ-N` references are checked by build agents at task execution; if
a task description doesn't address a situation, the build agent
falls back to the OQ default.

| OQ | Question | Default behavior |
|---|---|---|
| **OQ-01** | Linux pod path prediction wrong | Hard-code the predicted constant; if View logs returns `log_unavailable` in prod, hotfix the constant and redeploy (env override per ADR-013 is also available) |
| **OQ-02** | CLI version bumps task_id format | Treat task_id as opaque string; only constrain via the validator regex `/^[a-zA-Z0-9_-]{1,20}$/`; never pin tighter shapes |
| **OQ-03** | 100+ bg tasks in one turn | No virtualization initially; revisit only if real-user reports lag |
| **OQ-04** | Large `<task-id>.output` files | REST `?tail=N` default 8192, max 65536; client requests multiple times if it needs more; no range request, no compression |
| **OQ-05** | Output file missing | REST returns 200 `{ status: "log_unavailable" }`; panel shows "No log captured"; panel itself keeps working |
| **OQ-06** | fsnotify watcher creation fails | Log WARN; the subscribe path returns success but no chunks; user sees only REST-tail snapshot |
| **OQ-07** | WS / panel close leaks watcher | React useEffect cleanup + server WS subscription map; T9 test asserts cleanup on disconnect |
| **OQ-08** | tool_use_id ≠ task_id (race / CLI bug) | BgTask still recorded; the chat-side Bash card just won't link to the panel entry; both stay functional |
| **OQ-09** | Node-shared /tmp uid collision | Resolved by ADR-007 (CLAUDE_CODE_TMPDIR); if user overrides env to point both pods at the same dir, that's their problem |
| **OQ-10** | Old spec at `docs/superpowers/specs/2026-06-19-ui-mode-...` | SUPERSEDED header already added in same commit as this file; spec stays for git-blame archaeology |

---

## Task DAG

17 tasks across 4 PRs. Independent subtrees can build concurrently.
`depends_on` arrows must be respected by the workflow scheduler.

```
PR-A (refactor)
  T1 ─── T2 ─── T3 ─── T4

PR-B (backend)
  T5 ────────────────────────────┐
  T6 ─── T7 ─── T8 ─── T9 ─── T10 ┤
  T11 ───────────────────────────┘
                                 │
PR-C (frontend, depends on PR-B) │
  T12 ─── T13 ─── T14            ┘
  T15 ───────────┘
                  │
PR-D (docs)       │
  T16 ────────────┘
```

T11 has no internal dependencies in PR-B and can build in parallel
with T5/T6/T7/T8/T9/T10. T15 has no dependency on T12-T14 and can
build in parallel.

### Global commands

```
GLOBAL TEST_CMD (frontend): cd web && npx vitest run --reporter=verbose
GLOBAL TEST_CMD (backend):  go test ./... -timeout 90s
GLOBAL LINT_CMD (frontend): cd web && npx tsc --noEmit && npx eslint src/
GLOBAL LINT_CMD (backend):  go vet ./... && test -z "$(gofmt -l .)"
GLOBAL BUILD_CMD (frontend): cd web && npm run build
GLOBAL BUILD_CMD (backend):  go build ./cmd/alfred-server
```

Each task's gate overrides scope to the touched area for speed.
Final pre-merge gate (before each PR is integrated) runs all four
GLOBAL commands.

---

### T1 — Merge asTaskStarted into asTaskPayload

**PR:** A. **Depends:** none. **ADR:** ADR-021.
**Files:** `web/src/features/sessions/claudeReducer.ts`.

**Spec:** Replace the `asTaskStarted` coercer (around line 628) and
the `task_started` switch arm (around line 338) with calls to a new
`asTaskPayload(kind, payload)` discriminated-union helper. Add the
helper above the existing task coercers. The helper signature:

```ts
type TaskPayloadStarted = {
  kind: 'task_started'
  taskId: string; toolUseId: string; description: string; taskType: string
}
type TaskPayloadNotification = {
  kind: 'task_notification'
  taskId: string; toolUseId: string; status: string; summary: string
}
type TaskPayloadUpdated = {
  kind: 'task_updated'
  taskId: string; status: string; endTime: number
}
type TaskPayload =
  | TaskPayloadStarted | TaskPayloadNotification | TaskPayloadUpdated

function asTaskPayload(kind: 'task_started', payload: unknown): TaskPayloadStarted
function asTaskPayload(kind: 'task_notification', payload: unknown): TaskPayloadNotification
function asTaskPayload(kind: 'task_updated', payload: unknown): TaskPayloadUpdated
function asTaskPayload(kind: TaskPayload['kind'], payload: unknown): TaskPayload
```

Wire format remains snake_case (CLI source); helper extracts to
camelCase. For T1: only the `task_started` case rewires; the other
two coercers (`asTaskNotification`, `asTaskUpdated`) stay in place
and are tackled in T2/T3 in turn.

**Gate:**
```
cd web && npx vitest run src/features/sessions/claudeReducer.test.ts \
  && npx tsc --noEmit \
  && ! grep -E 'function asTaskStarted\(' src/features/sessions/claudeReducer.ts
```
All existing tests pass. The `function asTaskStarted` declaration is
gone. `asTaskNotification` and `asTaskUpdated` still present (T2/T3
remove them).

---

### T2 — Merge asTaskNotification into asTaskPayload

**PR:** A. **Depends:** T1. **ADR:** ADR-021.
**Files:** `web/src/features/sessions/claudeReducer.ts`.

**Spec:** Same pattern as T1 for `task_notification`. The `task_notification`
switch arm body's only change is `asTaskNotification(payload)` →
`asTaskPayload('task_notification', payload)`. The behavior comment
about "Some CLIs emit the final 'completed' status on
task_notification" stays.

**Gate:**
```
cd web && npx vitest run src/features/sessions/claudeReducer.test.ts \
  && npx tsc --noEmit \
  && ! grep -E 'function asTaskNotification\(' src/features/sessions/claudeReducer.ts
```

---

### T3 — Merge asTaskUpdated into asTaskPayload

**PR:** A. **Depends:** T2. **ADR:** ADR-021.
**Files:** `web/src/features/sessions/claudeReducer.ts`.

**Spec:** Same pattern as T1/T2 for `task_updated`.

**Gate:**
```
cd web && npx vitest run src/features/sessions/claudeReducer.test.ts \
  && npx tsc --noEmit \
  && ! grep -E 'function asTaskUpdated\(' src/features/sessions/claudeReducer.ts
```

---

### T4 — types.ts doc updates + BG_TASK_TOOL_NAMES + 3 regression tests

**PR:** A. **Depends:** T3. **ADR:** ADR-016, ADR-010.
**Files:** `web/src/features/sessions/types.ts`,
`web/src/features/sessions/claudeReducer.test.ts`.

**Spec:**

1. In `types.ts`, replace the `BgTask` doc comment (around line 110)
   with:
   ```ts
   // BgTask tracks one Claude-CLI-spawned background task.
   // Producers observed: Monitor's detached bash,
   // Bash(run_in_background=true). Lifecycle is owned exclusively by
   // the CLI: it spawns, monitors, and SIGKILLs in-flight tasks when
   // the parent `claude -p` exits (status="killed"). alfred-server
   // is the observer, not the parent.
   //
   // External-resource reference — not persisted in snapshot.json;
   // re-derived on alfred-server restart from .jsonl replay (every
   // in_progress task is forced to status="killed"). See ADR-001,
   // ADR-018, and DESIGN.md.
   ```

2. Replace the `bgTaskId` doc comment (around line 105) with:
   ```ts
   // CLI background-task id, set when a matching task_started event
   // arrives. Links this tool block to bgTasks[bgTaskId]. Set for
   // any tool that emits task_started (Monitor, Bash, future
   // producers).
   ```

3. Above `export interface BgTask`, add:
   ```ts
   export const BG_TASK_TOOL_NAMES = ['Monitor', 'Bash'] as const
   export type BgTaskToolName = (typeof BG_TASK_TOOL_NAMES)[number]
   ```

4. Widen `BgTask['status']` to
   `'in_progress' | 'completed' | 'failed' | 'killed' | 'stopped'`.

5. Append three regression tests to `claudeReducer.test.ts`:
   - `task_started decodes snake_case wire to camelCase state` —
     drives `applyClaudeEvent` with a snake_case payload, asserts
     camelCase BgTask fields.
   - `task_notification preserves snake_case → camelCase status
     mapping` — runs `task_started` then `task_notification` with
     `status: "completed"`, asserts `lastEventSummary` and `status`
     map correctly.
   - `task_updated decodes nested patch.end_time` — runs
     `task_started` then `task_updated` with
     `patch: { status: "completed", end_time: 1781706910801 }`,
     asserts `finishedAt` is the ISO string of that epoch ms.

**Gate:**
```
cd web && npx vitest run src/features/sessions/claudeReducer.test.ts \
  && npx tsc --noEmit \
  && grep -q "BG_TASK_TOOL_NAMES" src/features/sessions/types.ts \
  && grep -q "killed when server restarted" src/features/sessions/types.ts \
     || true   # comment-text check; remove "|| true" if literal match achievable
```
(The grep on doc text is best-effort; the structural assertions are
the test pass + tsc clean.)

---

### T5 — Bootstrap CLAUDE_CODE_TMPDIR and ALFRED_CLAUDE_BG_TASK_DIR

**PR:** B. **Depends:** none. **ADR:** ADR-007.
**Files:** `cmd/alfred-server/main.go` (or wherever
`os.Setenv("CLAUDE_CODE_TMPDIR", ...)` belongs — likely a new helper
in `internal/claudebgtasks/env.go`); test file
`internal/claudebgtasks/env_test.go`.

**Spec:**

Create `internal/claudebgtasks/env.go`:

```go
package claudebgtasks

import (
    "fmt"
    "os"
    "path/filepath"
    "strconv"
)

// Bootstrap establishes the two env vars that bind alfred-server's
// view of bg-task file paths to the CLI's. Call once from main()
// after the data dir is known and before any claude -p is spawned.
//
// CLAUDE_CODE_TMPDIR is read by Claude CLI to decide where to put
// its per-uid scratch root. ALFRED_CLAUDE_BG_TASK_DIR is read by
// our path resolver to find log files. Setting both here keeps them
// from drifting.
//
// If the user has set ALFRED_CLAUDE_BG_TASK_DIR explicitly, that
// override wins (we do NOT also override CLAUDE_CODE_TMPDIR in that
// case — operator is responsible for consistency).
func Bootstrap(dataDir string) error {
    bgTmpRoot := filepath.Join(dataDir, "claude-tmp")
    if err := os.MkdirAll(bgTmpRoot, 0700); err != nil {
        return fmt.Errorf("create bg tmp root: %w", err)
    }
    if existing := os.Getenv("ALFRED_CLAUDE_BG_TASK_DIR"); existing != "" {
        return nil // operator-overridden
    }
    if err := os.Setenv("CLAUDE_CODE_TMPDIR", bgTmpRoot); err != nil {
        return fmt.Errorf("set CLAUDE_CODE_TMPDIR: %w", err)
    }
    uid := os.Getuid()
    bgTaskDir := filepath.Join(bgTmpRoot, "claude-"+strconv.Itoa(uid))
    if err := os.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", bgTaskDir); err != nil {
        return fmt.Errorf("set ALFRED_CLAUDE_BG_TASK_DIR: %w", err)
    }
    return nil
}
```

Wire into `cmd/alfred-server/main.go`: call `claudebgtasks.Bootstrap(dataDir)`
after the data dir is set and before `runner.NewRunner()` constructs
the claude runner.

`env_test.go` covers:
1. Default case: CLAUDE_CODE_TMPDIR + ALFRED_CLAUDE_BG_TASK_DIR
   both set, point to consistent paths.
2. Override case: ALFRED_CLAUDE_BG_TASK_DIR pre-set →
   CLAUDE_CODE_TMPDIR NOT modified.
3. `bgTmpRoot` directory is created with mode 0700.

**Gate:**
```
go test ./internal/claudebgtasks/... -v -run "TestBootstrap" && go vet ./...
```

---

### T6 — claudebgtasks/path.go path resolver

**PR:** B. **Depends:** T5. **ADR:** ADR-006, ADR-013.
**Files:** `internal/claudebgtasks/path.go`,
`internal/claudebgtasks/path_test.go`.

**Spec:**

Implement the path resolver exactly matching the CLI's `Xj` rule:

```go
package claudebgtasks

import (
    "os"
    "path/filepath"
    "strconv"

    "golang.org/x/text/unicode/norm" // already in go.mod? if not, add
)

// MangleCwd implements Claude CLI's cwd → path-segment encoding.
// 1. NFC-normalise the input.
// 2. realpath-resolve (caller's responsibility — we receive the
//    resolved cwd; ADR-008 says handleClaudePrompt already does this).
// 3. Replace every non-[a-zA-Z0-9] char with '-'.
// 4. If the result is > 200 chars, truncate to 200 + "-" + base-36
//    Math.abs(javaHash(originalCwd)).
//
// `originalCwd` is the post-NFC, post-realpath path. The hash input
// is `originalCwd` (NOT the truncated string).
func MangleCwd(realpathCwd string) string {
    nfc := norm.NFC.String(realpathCwd)
    mangled := make([]byte, 0, len(nfc))
    for _, r := range nfc {
        if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
            mangled = append(mangled, byte(r))
        } else {
            mangled = append(mangled, '-')
        }
    }
    if len(mangled) > 200 {
        h := javaHash(nfc)
        if h < 0 { h = -h }
        suffix := strconv.FormatInt(int64(h), 36)
        return string(mangled[:200]) + "-" + suffix
    }
    return string(mangled)
}

// javaHash matches Claude CLI's reducer: h = ((h<<5)-h+ch)|0,
// reduced over the string's UTF-16 code units. Because Go strings
// are UTF-8, we iterate runes and re-emit UTF-16 surrogate pairs
// as needed. Returns the 32-bit truncated result as an int32.
func javaHash(s string) int32 {
    var h int32
    for _, r := range s {
        if r <= 0xFFFF {
            h = (h<<5 - h + int32(r))
        } else {
            // Surrogate pair
            rmin := r - 0x10000
            hi := int32(0xD800 + (rmin >> 10))
            lo := int32(0xDC00 + (rmin & 0x3FF))
            h = (h<<5 - h + hi)
            h = (h<<5 - h + lo)
        }
    }
    return h
}

// OutputPath returns the file path where Claude CLI writes a given
// bg task's stdout. Reads ALFRED_CLAUDE_BG_TASK_DIR; if unset,
// falls back to "/tmp/claude-<uid>" (matching the CLI's own
// default).
func OutputPath(realpathCwd, sessionUUID, taskID string) string {
    base := os.Getenv("ALFRED_CLAUDE_BG_TASK_DIR")
    if base == "" {
        base = filepath.Join("/tmp", "claude-"+strconv.Itoa(os.Getuid()))
    }
    return filepath.Join(base, MangleCwd(realpathCwd), sessionUUID, "tasks", taskID+".output")
}
```

`path_test.go` includes a `TestMangleCwd_GoldenCases` table-driven
test with these 7 cases sampled from the spike:

```go
cases := []struct {
    name, cwd, expected string
}{
    {"deep_path", "/Users/jesseliu/Desktop/Chore/Headless-Alfred",
        "-Users-jesseliu-Desktop-Chore-Headless-Alfred"},
    {"tmp_realpath", "/private/tmp",
        "-private-tmp"},
    {"single_segment", "/Users/jesseliu",
        "-Users-jesseliu"},
    {"with_space_and_dot", "/path with space/foo.bar",
        "-path-with-space-foo-bar"},
    {"trailing_slash", "/Users/jesseliu/",
        "-Users-jesseliu-"},
    {"root", "/",
        "-"},
    // 201-char path triggers truncation; suffix is base-36 javaHash.
    {"truncation", "/" + strings.Repeat("a", 200),
        // expected: "-" + "a" * 199 → 200 chars exactly → no truncation.
        // Need a 201+ char input; let's use /aaa...(200 a's) = 201 chars total.
        // After mangle: "-" + "aaa...(200 a's)" = 201 chars → truncate.
        "-" + strings.Repeat("a", 199) + "-" + computedHash201A,
    },
}
```

(The last case's `computedHash201A` should be computed by the test
itself by calling `javaHash`, not pasted as a magic constant.)

Plus `TestOutputPath` covering: default base (env unset), env-set base.

**Gate:**
```
go test ./internal/claudebgtasks/... -v -run "TestMangleCwd|TestOutputPath" && go vet ./...
```

---

### T7 — applyTaskUpdated handles killed + stopped

**PR:** B. **Depends:** T6. **ADR:** ADR-010, ADR-016.
**Files:** `internal/claudestate/state.go`,
`internal/claudestate/state_test.go`.

**Spec:**

Modify `applyTaskUpdated` (`state.go:471`) so the status check
includes both new values:

```go
func (s *SessionState) applyTaskUpdated(p *TaskUpdatedPayload, ts time.Time) {
    bt, ok := s.state.BgTasks[p.TaskID]
    if !ok {
        return
    }
    status, _ := p.Patch["status"].(string)
    if status != "completed" && status != "failed" &&
       status != "killed" && status != "stopped" {
        return
    }
    bt.Status = status
    if et, ok := p.Patch["end_time"].(float64); ok && et > 0 {
        bt.FinishedAt = timePtr(time.Unix(0, int64(et)*int64(time.Millisecond)).UTC())
    } else {
        bt.FinishedAt = timePtr(ts)
    }
    if status == "killed" {
        bt.LastEventSummary = "killed by Claude on turn end"
    }
    s.state.BgTasks[p.TaskID] = bt
}
```

Add two unit tests:
- `TestApplyTaskUpdated_KilledSetsStatusAndSummary` — patch with
  status="killed" → BgTask.Status=="killed",
  BgTask.LastEventSummary=="killed by Claude on turn end".
- `TestApplyTaskUpdated_StoppedSetsStatus` — patch with status="stopped"
  → BgTask.Status=="stopped"; LastEventSummary not overwritten if
  already set by a prior notification.

**Gate:**
```
go test ./internal/claudestate/ -v -run "TestApplyTaskUpdated"
```

---

### T8 — REST endpoint GET /api/sessions/{sid}/bg-tasks/{taskId}/log

**PR:** B. **Depends:** T7. **ADR:** ADR-015, ADR-013.
**Files:** `internal/api/bg_task_log_handler.go` (new),
`internal/api/bg_task_log_handler_test.go` (new),
`internal/api/router.go` (route registration).

**Spec:**

Handler signature:

```go
// GetBgTaskLogHandler serves the tail of a bg-task's stdout file.
// Query: ?tail=N (default 8192, max 65536). Returns JSON
// { "bytes": "<base64>", "size": <int>, "truncated": <bool> } on
// success, or { "status": "log_unavailable", "reason": "<short>" }
// if the file can't be found / read. Both cases are HTTP 200; the
// frontend distinguishes by the status field.
func GetBgTaskLogHandler(mgr SessionMetaResolver, stateMgr *claudestate.SessionManager) http.Handler
```

Path lookup:
1. Look up the `SessionMeta.ClaudeSessionID` and the realpath-cwd
   (whatever `handleClaudePrompt` passes to runner — see ADR-008).
2. Call `claudebgtasks.OutputPath(realpathCwd, sessionUUID, taskID)`.
3. `os.Stat` the file. If missing: return `{ status:
   "log_unavailable", reason: "file_not_found" }`.
4. `os.Open`, `Seek(-tail, io.SeekEnd)` (clamped at file size), read
   `tail` bytes, base64-encode, return `{ bytes, size, truncated:
   size > tail }`.

taskID validation: `regexp.MustCompile(\`^[a-zA-Z0-9_-]{1,20}$\`)`.
Reject non-matching with 400.

Tests:
- `TestBgTaskLogHandler_NotFound` — file doesn't exist → 200,
  status=log_unavailable.
- `TestBgTaskLogHandler_SuccessfulTail` — write a file with > tail
  bytes, request → returns the last tail bytes base64-decoded
  matches.
- `TestBgTaskLogHandler_InvalidTaskID` — `..` in taskID, very long,
  control chars → 400.
- `TestBgTaskLogHandler_TailDefault` — no tail param → uses 8192.
- `TestBgTaskLogHandler_TailClamped` — tail=999999 → clamped to
  65536.

**Gate:**
```
go test ./internal/api/ -v -run "TestBgTaskLogHandler"
```

---

### T9 — WS subscribe_bg_task_log + fsnotify watcher

**PR:** B. **Depends:** T8. **ADR:** ADR-014.
**Files:** `internal/api/ws.go` (or a new file
`internal/api/bg_task_log_subscribe.go`),
`internal/api/bg_task_log_subscribe_test.go` (new).

**Spec:**

Add an inbound WS frame handler for `subscribe_bg_task_log`:

```go
type SubscribeBgTaskLogPayload struct {
    TaskID string `json:"taskId"`
}
```

Maintain a per-WS map `map[taskID]*fsnotify.Watcher` plus a
goroutine per subscription. On `subscribe`:
1. Validate taskID (same regex as T8).
2. Resolve output file path (same code path as T8).
3. `fsnotify.NewWatcher`; `watcher.Add(path)`.
4. Open file, `Seek(0, io.SeekEnd)` (NOT tail — the REST endpoint
   gave the user the tail; we only stream new appends from
   subscription point onward).
5. Spawn a goroutine that on each `fsnotify.Write` event reads from
   the file's current position to EOF, base64-encodes, and emits a
   `bg_task_stdout_chunk` frame via the same `write` closure
   `runClientLoop` uses.

On `unsubscribe_bg_task_log` or WS close: close watcher, return
goroutine.

OQ-06 fallback: if `fsnotify.NewWatcher` fails or `watcher.Add` fails,
log WARN and return a `bg_task_stdout_chunk { taskId, status:
"watcher_unavailable" }` (status overload — frontend treats this as
"no live stream").

Tests:
- `TestWS_SubscribeBgTaskLog_StreamsAppendedBytes` — write to file
  after subscribe, assert a chunk frame arrives.
- `TestWS_SubscribeBgTaskLog_CleansUpOnUnsubscribe` — subscribe,
  unsubscribe, write to file, assert no more chunks.
- `TestWS_SubscribeBgTaskLog_CleansUpOnDisconnect` — subscribe,
  drop WS, assert watcher Close called (via test hook).

**NO sleep / time.Sleep** in tests; use `sync.WaitGroup` or
channels for synchronization.

**Gate:**
```
go test ./internal/api/ -v -run "TestWS_SubscribeBgTaskLog" -count=3
```

(`-count=3` to catch flakiness — must pass all 3 iterations.)

---

### T10 — bg_task_stdout_chunk frame emit

**PR:** B. **Depends:** T9. **ADR:** ADR-020.
**Files:** `internal/api/wsproto.go`,
`internal/api/bg_task_log_subscribe_test.go` (extend).

**Spec:**

In `wsproto.go`, extend the `OutMsg` union with:

```go
type BgTaskStdoutChunkPayload struct {
    TaskID string `json:"taskId"`
    Bytes  string `json:"bytes"`  // base64
    Status string `json:"status,omitempty"`  // optional: "watcher_unavailable"
}
```

Add to the `OutMsg` type tag list as `bg_task_stdout_chunk`.

Frontend `web/src/lib/ws.ts` ServerMsg union must mirror this — but
that change happens in T12 (frontend reducer task).

Test in T9's file gets extended:
- `TestWS_BgTaskStdoutChunk_PayloadShape` — sanity check the JSON
  envelope: `type == "bg_task_stdout_chunk"`, `payload.taskId`
  present, `payload.bytes` is base64.

**Gate:**
```
go test ./internal/api/ -v -run "TestWS_BgTask"
```

---

### T11 — claudestate.Loader rebuilds BgTasks from .jsonl

**PR:** B. **Depends:** none (independent of T5-T10). **ADR:** ADR-018.
**Files:** `internal/claudestate/loader.go`,
`internal/claudestate/loader_test.go`.

**Spec:**

Extend `Loader.Load` (the part that merges jsonl + snapshot) so that
after the existing merge, it walks the jsonl again and applies every
`task_started` / `task_notification` / `task_updated` event to the
ClaudeState as if it were running through `state.Apply`. Reuse the
existing reducers — don't reimplement.

After replay, iterate `state.BgTasks` and for any entry with
`Status == "in_progress"`, force:
- `Status = "killed"`
- `LastEventSummary = "killed when server restarted"`
- `FinishedAt = now()` (best-effort timestamp; the real finishedAt
  was unrecoverable).

Tests:
- `TestLoader_RebuildBgTasksFromJsonl_MarksInProgressAsKilled` —
  golden jsonl with one task_started, no task_updated → after Load,
  BgTask present with status=killed.
- `TestLoader_RebuildBgTasksFromJsonl_PreservesCompletedStatus` —
  golden jsonl with task_started + task_updated (completed) → after
  Load, BgTask status=completed (NOT overwritten).
- `TestLoader_RebuildBgTasks_HonorsSubagentsNoRebuildRule` — golden
  jsonl with SubagentStart + SubagentStop → after Load, Subagents
  map is empty (ADR-009).

**Gate:**
```
go test ./internal/claudestate/ -v -run "TestLoader_RebuildBgTasks|TestLoader_.*Subagents"
```

---

### T12 — Frontend reducer for new statuses + bg_task_stdout_chunk

**PR:** C. **Depends:** T4 (asTaskPayload), T7 (Go side), T10 (WS frame).
**ADR:** ADR-016, ADR-020.
**Files:** `web/src/features/sessions/claudeReducer.ts`,
`web/src/features/sessions/types.ts`,
`web/src/lib/ws.ts`,
`web/src/features/sessions/claudeReducer.test.ts`.

**Spec:**

1. `web/src/lib/ws.ts`: add `'bg_task_stdout_chunk'` to the
   ServerMsg type union; payload type
   `{ taskId: string; bytes: string; status?: 'watcher_unavailable' }`.

2. `claudeReducer.ts`: handle `bg_task_stdout_chunk` by appending
   base64-decoded bytes to a per-task log buffer in PerSession state.
   Buffer is capped at 64KB tail (drop from head when over).

3. Already-merged switch arms (`task_started`, `task_notification`,
   `task_updated`) work as-is for `killed`/`stopped` because they
   just stash the status string. No additional change to those arms
   in this task.

4. `types.ts`: add to PerSession.claude state:
   ```ts
   bgTaskLogs: Record<string, string>  // taskId → tail buffer
   ```
   Initialize to `{}` in `EmptyClaudeState` equivalent.

5. Tests in `claudeReducer.test.ts`:
   - `bg_task_stdout_chunk appends decoded bytes to per-task buffer`.
   - `bg_task_stdout_chunk caps buffer at 64KB`.
   - `task_updated.status=killed lands in bgTasks.status`.

**Gate:**
```
cd web && npx vitest run src/features/sessions/claudeReducer.test.ts \
  && npx tsc --noEmit
```

---

### T13 — BackgroundTasksPanel component

**PR:** C. **Depends:** T12. **ADR:** ADR-016, ADR-019.
**Files:** `web/src/features/sessions/BackgroundTasksPanel.tsx` (new),
`web/src/features/sessions/BackgroundTasksPanel.css` (new),
`web/src/features/sessions/BackgroundTasksPanel.test.tsx` (new).

**Spec:**

Component contract:

```ts
interface BackgroundTasksPanelProps {
  bgTasks: Record<string, BgTask>
  subagents: Record<string, SubagentEntry>
  inFlight: boolean
  bgTaskLogs: Record<string, string>
  onSubscribeLog: (taskId: string) => void
  onUnsubscribeLog: (taskId: string) => void
  onFetchLogTail: (taskId: string) => Promise<{ bytes: string; size: number; truncated: boolean } | { status: 'log_unavailable' }>
  open: boolean
  onToggle: () => void
  connState: 'open' | 'connecting' | 'closed'
  turnsLoaded: boolean
}
```

Render (when `open === true`):
- Header row: `Main Claude: ● running` (if inFlight) / `○ idle`
  (if !inFlight && bgTasks empty) / `✗ exited` (if !inFlight && any
  bgTask was killed). Then `Live · N running` (or red banner if
  connState !== 'open').
- **Background bash** group: bgTasks where status=='in_progress',
  no tool-name filter (NO 'Monitor' hardcode).
- **Subagents (blocking main Claude)** group: subagents where
  finishedAt is unset AND inFlight===true. If !inFlight, hide group.
- **Recently finished** group: bgTasks where status !== 'in_progress'
  AND `Date.now() - Date.parse(finishedAt) < 60_000`.

Each row:
- Description (or task_input.command preview if description empty).
- Chip: tool name + status (visual per ADR-016).
- Elapsed (running) or finished-ago (done).
- "View logs" button toggles a `<pre>` showing
  bgTaskLogs[taskId] (initial REST tail + WS chunks).
- Expanded body: tool input JSON + lastEventSummary.

Render (when `open === false || !turnsLoaded`):
- Nothing, OR a `Loading…` skeleton if `open && !turnsLoaded`.

Tests:
- `renders 0 from empty maps and disables interactions`
- `N counts bgTasks+subagents in_progress only`
- `Bash task without Monitor name still renders` (regression)
- `60s fade uses Date diff, not setTimeout`
- `panel hidden when !turnsLoaded shows skeleton`
- `View logs button triggers onFetchLogTail then onSubscribeLog`
- `unmount triggers onUnsubscribeLog for every active log subscription`
  (OQ-07 leak prevention)

**NO real fsnotify or real fetch in tests** — mocks everywhere; this
is a pure-render component test.

**Gate:**
```
cd web && npx vitest run src/features/sessions/BackgroundTasksPanel.test.tsx \
  && npx tsc --noEmit
```

---

### T14 — WorkspacePage header badge + state wiring

**PR:** C. **Depends:** T13. **ADR:** ADR-017, ADR-020.
**Files:** `web/src/features/sessions/WorkspacePage.tsx`,
`web/src/features/sessions/useSessions.ts` (add subscribe/unsubscribe
WS senders + REST log fetcher).

**Spec:**

1. In `useSessions.ts`, add:
   ```ts
   subscribeBgTaskLog(sid: string, taskId: string): void
   unsubscribeBgTaskLog(sid: string, taskId: string): void
   fetchBgTaskLogTail(sid: string, taskId: string, tail?: number): Promise<...>
   ```
   The subscribe/unsubscribe just `send` the WS frame. fetchLogTail
   does a REST `GET /api/sessions/{sid}/bg-tasks/{taskId}/log?tail=N`.

2. In `WorkspacePage.tsx`:
   - Add header button (in `workspace__header-right`) styled as
     `[⚙ N Background tasks]`. Disabled when N === 0.
   - State: `const [bgTasksOpen, setBgTasksOpen] = useState(() =>
     localStorage.getItem('alfred_bg_tasks_panel_open') === 'true')`.
     On change, write to localStorage.
   - When open and current session is in UI mode, render
     `<BackgroundTasksPanel ... />` as a floating card below the
     header (absolute-positioned, width 440px, does not perturb
     workspace gridTemplateColumns).

Test in `WorkspacePage.test.tsx`:
- `badge counts running bgTasks+subagents`
- `clicking badge toggles panel and persists to localStorage`
- `panel only renders in UI mode (mode === 'claude' && renderer === 'ui')`

**Gate:**
```
cd web && npx vitest run src/features/sessions/WorkspacePage.test.tsx \
  && npx tsc --noEmit
```

---

### T15 — TurnStatsLine un-hardcode

**PR:** C. **Depends:** T4 (BG_TASK_TOOL_NAMES). **ADR:** ADR-016.
**Files:** `web/src/features/sessions/ClaudeChatView.tsx`,
`web/src/features/sessions/ClaudeChatView.test.tsx`.

**Spec:**

In `ClaudeChatView.tsx`:

1. `TurnStatsLine` (~line 361): remove
   `if (!turn.done) return null` — render mid-turn too.
2. Replace `tool.name === 'Monitor'` filter with: any tool block
   that has a `bgTaskId`. Iterate
   `turn.blocks.filter((b) => b.kind === 'tool' && b.tool.bgTaskId)`.
3. The text changes from "X Monitor tasks (Y running)" to
   "X background tasks (Y running)" — generic.
4. Subagents text changes from "X subagents (running)" to
   "X subagents (blocking)" — more accurate per ADR.

Test:
- `TurnStatsLine renders mid-turn` (regression for the gate
  removal).
- `Bash background tasks counted alongside Monitor` (regression for
  the hardcode removal).

**Gate:**
```
cd web && npx vitest run src/features/sessions/ClaudeChatView.test.tsx \
  && npx tsc --noEmit
```

---

### T16 — CONTEXT.md trap entries + Go BgTask doc + Why-these-choices

**PR:** D. **Depends:** all of A/B/C. **ADR:** ADR-001, ADR-007,
ADR-014, ADR-016, ADR-018.
**Files:** `CONTEXT.md`, `internal/claudestate/types.go`.

**Spec:**

Append to the "Non-obvious traps" table in CONTEXT.md (4 new rows):

```
| Claude CLI is the SOLE owner of Bash(run_in_background) lifecycle. It parents them as children (not detached via setsid) and SIGKILLs every in-flight task when `claude -p` exits, emitting `task_updated.patch.status="killed"`. alfred is a pure observer. | A future engineer adds a "Stop bg task" button or a tmux-foster scheme; CLI's own state diverges and re-emits stale events; users see duplicate "killed" events or stalled UIs | `TestApplyTaskUpdated_KilledSetsStatusAndSummary` + DESIGN.md ADR-001 |
| `CLAUDE_CODE_TMPDIR` is hardcoded to `/tmp/claude-<uid>` if unset (NOT `os.TempDir()`). On a Kubernetes node sharing `/tmp` across pods of the same uid, two alfred pods would silently share the bg-task scratch root, causing the second pod's ownership check (mode 0700) to fail. alfred-server's bootstrap sets it to `<dataDir>/claude-tmp` to namespace. | The first deploy after a `runAsUser` change shows "permission denied" on `mkdir /tmp/claude-1000/...` from claude -p, but the message disappears mid-stream-json and the only symptom is empty bg-task log files | `TestBootstrap_OperatorOverride` + DESIGN.md ADR-007 |
| BgTask `Status` enum has 5 values: `in_progress`, `completed`, `failed`, `killed`, `stopped`. Adding a 6th means updating the Go reducer, the TS reducer, the visual map in BackgroundTasksPanel, AND the Loader's "force in_progress to killed" rule. | Adding e.g. `paused` and only updating the Go side; the panel chip renders unstyled (no CSS class for unknown status) and Loader still force-kills it | `TestApplyTaskUpdated_KilledSetsStatusAndSummary` + visual inspection of BackgroundTasksPanel.tsx status map |
| BgTasks are re-derived on Loader.Load by replaying jsonl task_started/notification/updated events; any task still `in_progress` after replay is FORCED to `killed` with summary "killed when server restarted". Subagents are NOT replayed (ADR-009). | A future engineer "fixes" the force-kill to preserve `in_progress` for "accuracy"; the panel lies, showing dead tasks as live | `TestLoader_RebuildBgTasksFromJsonl_MarksInProgressAsKilled` + `TestLoader_RebuildBgTasks_HonorsSubagentsNoRebuildRule` |
```

Append to the "Why these choices" table:

```
| Bg-task lifecycle is CLI-owned; alfred is observer-only | Manual spike with `claude -p` 2.1.169 on macOS confirmed CLI parents bg bashes as children and SIGKILLs them at `claude -p` exit. A detached-bash + alfred-fosters model would require either tmux containerization (CLI still kills) or `updatedInput` command rewriting (relies on an unverified CLI feature) — both fragile. Observer-only model is honest and works against the CLI as shipped. |
```

Update `internal/claudestate/types.go` `BgTask` Go doc comment to
match the TS update from T4:

```go
// BgTask tracks one Claude-CLI-spawned background task. Producers
// observed: Monitor's detached bash, Bash(run_in_background=true).
// Lifecycle is owned exclusively by the CLI: it spawns, monitors,
// and SIGKILLs in-flight tasks when the parent `claude -p` exits
// (status="killed"). alfred-server is the observer, not the parent.
//
// External-resource reference — not persisted in snapshot.json;
// re-derived on alfred-server restart from .jsonl replay (every
// in_progress task is forced to status="killed"). See ADR-001,
// ADR-018, and DESIGN.md.
```

**Gate:**
```
git diff CONTEXT.md | grep -q "CLAUDE_CODE_TMPDIR is hardcoded" \
  && git diff CONTEXT.md | grep -q "claude-tmp" \
  && git diff CONTEXT.md | grep -q "alfred is a pure observer" \
  && grep -q "killed when server restarted" internal/claudestate/types.go
```

---

## Post-build manual verification

After PRs A/B/C/D are merged, the human runs **T17 manually**:

1. Start alfred-server locally with a fresh `dataDir`.
2. Open UI mode for any session.
3. Prompt: "Use Bash run_in_background=true to start: `sleep 30 && echo hi > /tmp/alfred-e2e-marker`. Then immediately say done."
4. Confirm panel shows "1 Background task" with status `in_progress`.
5. Wait until Claude says done. Confirm panel shows the task as `killed`.
6. Press F5. Confirm panel still shows the task as `killed` after
   the page reload.
7. Open View logs. Confirm the log content is what the task wrote
   (empty for `sleep`; for echo-based tasks, the echoed text).

Acceptance: all 7 steps pass on the human's machine before PR-D is
considered green and the feature shipped.

---

## Workflow handoff

This file is the input to `feature-factory.workflow.js`. The
workflow's `args.tasks` translates each task above to one DAG node;
each `gate` becomes the Tester's gate command. `depends_on` is
explicit per task.

Token budget for this build: **+800k** (covers the implementer +
tester + supervisor + integrator for 16 tasks across 4 PRs, with
~3 review rounds slack).

Permissions allow-list (to add via update-config skill before launching
workflow): `go test`, `go vet`, `go build`, `gofmt`, `cd web`,
`npx vitest`, `npx tsc`, `npx eslint`, `npm run`, `git worktree`,
`git fetch`, `git commit`, `git push`, `git rebase`, `git merge`,
`gh pr create`, `gh pr merge`, `gh pr view`.
