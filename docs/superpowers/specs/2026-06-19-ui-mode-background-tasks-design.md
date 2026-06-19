# UI mode Background Tasks — Server-Managed via tmux — Design

**Date:** 2026-06-19
**Status:** Approved (brainstorm phase)
**Branch:** TBD (post-spec branch name)

## Problem

In UI mode, when Claude invokes `Bash(run_in_background=true)` (or the
`Monitor` tool, which uses the same mechanism), the CLI forks a detached
bash. The server is currently blind to that process after the parent
`claude -p` exits, with three concrete consequences:

1. **`task_started` after server-side failure.** `task_started` /
   `task_notification` / `task_updated` events arrive on `claude -p`'s
   stdout (stream-json). When `claude -p` exits — happy path included —
   the stream closes. The detached bash's later state (completed,
   failed, still running) reaches no one. `BgTasks[id]` is frozen at
   whatever the last event said, typically `in_progress`.

2. **Subagent semantics are misleading.** `Task(subagent_type=...)` is
   synchronous; the main Claude blocks on it. A panel that lists
   subagents as "background workers" misrepresents the CLI's contract.

3. **Server restart clears all background tasks.** `claudestate.BgTasks`
   is `external-resource reference — not persisted`. After alfred-server
   restart, the OS-level bashes are still running but their metadata
   (description, taskType, startedAt) is gone, and there is no path to
   re-associate them.

There is also no global UI surface for background tasks. The current
`ClaudeChatView` only renders bgTasks inline per-turn, gated on
`turn.done === true`, and hard-coded to `tool.name === 'Monitor'`, so
`Bash(run_in_background)` is invisible in-progress.

This document defines the end-to-end fix: server takes ownership of UI
mode's claude process by running it inside the alfred session's existing
tmux session (in a dedicated `claude-ui` window). Background bashes
forked by claude become long-lived members of that window. alfred-server
acts as the observer, not the parent process. tmux is the ownership
anchor — alfred-server can restart without disturbing in-flight work.

## Goals

- **Solve trap 1**: `BgTasks` lifecycle does not depend on a single
  `claude -p` invocation. State stays observable for the lifetime of
  the tmux session.
- **Solve trap 3**: alfred-server restart preserves the tmux session,
  the `claude-ui` window, and every background bash inside it. After
  restart, `BgTasks` is re-derived from the per-turn jsonl + a tmux
  probe; running tasks reconnect to live state.
- **Honestly surface trap 2**: subagents render in a separate group
  labelled to convey "main Claude is blocked on this", and are hidden
  once main Claude has exited.
- Add a global Background Tasks panel (header badge + flyout) usable at
  any time, regardless of which turn is being rendered.
- User can **Stop** a background task and **View logs** for it.
- Refresh parity: every panel pixel — task list, status, elapsed,
  panel-open flag — survives page refresh.

## Non-goals

- Solving the synchronous nature of `Task(subagent_type)`. The CLI
  protocol is fixed; subagents stay synchronous.
- TUI mode background visibility. TUI mode's "1 shell · ↓ to manage"
  state stays internal to Claude's TUI rendering. Users wanting
  observability switch to UI mode (documented in CONTEXT.md).
- Cross-`pod`-restart durability. `kubectl rollout` kills the tmux
  server. Spec §1 non-goals from the original headless-alfred design
  still apply.
- Persisting `BgTasks` into snapshot.json. They remain "external-
  resource reference"; the new spec only adds a *re-derivation* path,
  not a persistence one.

## Architecture

### tmux topology per alfred session

```
tmux session "<alfred-session-id>"
  ├── window 0 "shell"
  │    └── bash  (user workspace — cwd/env/aliases, unchanged)
  │         └── TUI mode claude  (when active — unchanged)
  │
  └── window 1 "claude-ui"        ← lazy, created on first UI-mode prompt
       └── bash  (purpose: host for claude -p invocations and the
                  background bashes they fork)
            ├── claude -p ... < .prompt > .jsonl  (one per prompt; short-lived)
            └── background bashes (claude -p's grandchildren, long-lived)
```

The shell window is the existing one created by `tmux_shell.go::Start`.
The `claude-ui` window is new, created on demand the first time a
session enters UI mode.

### Invariant additions

Existing invariant #1 (Bash lifecycle ≠ WS lifecycle) stays. New
invariant #5 — see CONTEXT.md changes section:

> **UI mode's `claude -p` and any background bashes it forks live in the
> alfred session's tmux session, specifically in the `claude-ui` window.
> alfred-server is the observer, not the parent process. The tmux
> session is the ownership anchor: alfred-server restart does not
> disturb in-flight work; tmux server death (only on pod restart) does.**

### Component layout

```
internal/
  claudeuiwindow/        ← new package
    window.go            EnsureWindow, StartPrompt, KillBgTask, ReadLogs
    sentinel.go          parse __ALFRED_DONE_<turn-id>_<exit-code>
    streampaths.go       per-turn .prompt / .jsonl / .stderr.log paths
  claude/
    runner.go            kept one release as fallback (gated by env
                         ALFRED_LEGACY_CLAUDE_RUNNER); removed in PR4
  api/
    claude_handlers.go   handleClaudePrompt: route through claudeuiwindow
    ws.go                add bg_task_stdout_chunk + bg_task_stopped frames
  claudestate/
    types.go             BgTask doc comment updated; field unchanged
    loader.go            new: re-derive BgTasks from jsonl + tmux probe on Load
web/
  features/sessions/
    BackgroundTasksPanel.tsx   ← new
    BackgroundTasksPanel.css   ← new
    ClaudeChatView.tsx         remove Monitor hardcode + turn-done gate
    claudeReducer.ts           merge three task coercers → asTaskPayload(kind)
    WorkspacePage.tsx          header badge + flyout wiring
    types.ts                   add BG_TASK_TOOL_NAMES, update BgTask doc
```

## Data flow

### One UI-mode prompt, end to end

```
[Frontend] user hits Enter
   ↓ WS "claude_prompt" frame (unchanged)
[Backend] handleClaudePrompt
   ↓
claudeuiwindow.EnsureWindow(alfredSID)
   - tmux list-windows -t <sid> | grep claude-ui ?
   - no → tmux new-window -t <sid> -n claude-ui
   - yes → noop
   ↓
write <dataDir>/sessions/<sid>/claude-ui/turn-<turnID>.prompt
   - content: the prompt body
   ↓
claudeuiwindow.StartPrompt(alfredSID, turnID, claudeArgs)
   - tmux send-keys -t <sid>:claude-ui -l \
       "nohup claude -p <claudeArgs> < .prompt > .jsonl 2>>.stderr.log;
        echo __ALFRED_DONE_<turnID>_$?"
   - tmux send-keys -t <sid>:claude-ui Enter
   - (nohup / setsid wrapping — exact form determined by spike; see below)
   ↓
StreamReader follows turn-<turnID>.jsonl  (offset file alongside it)
   - each line → claudestate.Apply (current path, unchanged)
   - including task_started / task_notification / task_updated
   - broadcast to WS clients (unchanged)
   ↓
Separate sentinel watcher: pipe-pane already captures claude-ui window
   output for shell-mode-style parsing. A new sentinel pattern
   __ALFRED_DONE_<turnID>_<exitCode> in that stream triggers the reaper
   path: dispatchClaudeRunEnded + InFlight=false
   ↓
bash in claude-ui window returns to prompt, ready for next send-keys
```

### Background bash spawned inside a turn

```
claude -p running in claude-ui window
   ↓ tool_use: Bash(run_in_background=true, command="long.sh")
   ↓ PreToolUse hook → bridge → dispatcher → user Allow (unchanged)
   ↓ CLI forks detached bash to run long.sh
     - because claude -p runs inside the tmux window's bash, the
       grandchild long.sh inherits the window's PTY / process group
     - SIGHUP-safety: handled by nohup/setsid in StartPrompt; long.sh
       outlives claude -p's exit
   ↓ task_started event in .jsonl → Apply → BgTasks["id"] = in_progress
   ↓ claude -p exits, sentinel fires, InFlight=false
   ↓ background bash continues running in claude-ui window
   ↓ next prompt: bash prompt returns, new claude -p spawns; background
     bash and new claude -p coexist as sibling processes under the
     window's bash
```

### Frontend rendering

The panel shows three groups:

- **Background bash**: every `BgTasks[*]` whose status is `in_progress`,
  grouped first. Any tool name (`Bash`, `Monitor`, future producers),
  no hardcoded filter.
- **Subagents (blocking main Claude)**: every `Subagents[*]` with
  `finishedAt == nil`. Group is hidden when `InFlight == false`
  (subagents can't outlive main Claude per CLI contract; any residue is
  hook race).
- **Recently finished**: completed entries from both maps, filtered by
  `Date.now() - finishedAt < 60_000`.

Empty groups don't render (no title either). The badge `⚙ N Background
tasks` counts `Background bash + Subagents` running. N=0 renders disabled.

## Server-side restart recovery

```
alfred-server starts
   ↓
Manager.Reconcile (existing)
   - tmux ListSessions → cross-reference with sessions.json
   - alive sessions kept, dead ones marked closed
   ↓
For each alive session:
   - claudeuiwindow.ResumeOpenTurns(sid)
     - tmux list-windows -t <sid> → does claude-ui exist?
     - no → user never used UI mode; nothing to resume
     - yes:
       - **Re-issue pipe-pane** to restore the sentinel-capture stream.
         alfred-server's prior pipe-pane writer died with the process;
         tmux stops piping when the writer exits. Without this step,
         the next __ALFRED_DONE sentinel and all subsequent bg-task
         stdout chunks would be lost. (See CONTEXT.md trap re:
         pipe-pane + restart, also applies here.)
       - For each <dataDir>/sessions/<sid>/claude-ui/turn-*.jsonl:
         - if matching <turnID>.done file does NOT exist:
           - StreamReader.Resume reads from persisted offset to EOF
           - re-emits Apply for any bytes written since shutdown
           - if no __ALFRED_DONE sentinel observed in pipe-pane buffer:
             call finalizeStaleTrailingTurn (per invariant #4):
             Done=true, IsError=true, FinishedAt=now,
             "Server restarted while this turn was running"
   - claudestate.Loader.RebuildBgTasksFromJsonlAndTmux(sid)
     - replay every task_* event in every <turnID>.jsonl → candidate set
     - tmux pane probe: resolve the claude-ui pane's bash PID via
       `tmux display-message -p -t <sid>:claude-ui '#{pane_pid}'`,
       then walk the process tree with `pgrep -P` recursively (or read
       `/proc/<pid>/task/<tid>/children` on Linux) to collect every
       descendant; intersect descendant cmdlines with each replayed
       BgTask's original Bash command to associate. Build live set.
     - for each candidate:
       - in live set → BgTasks[id] = {…replayed…, status: "in_progress"}
       - not in live set → BgTasks[id] = {…replayed…, status: "exited",
         lastEventSummary: "(exited during server downtime)"}
   - **Subagents map is NOT rebuilt.** Subagents require a live main
     Claude per CLI contract (Task tool is synchronous). After
     alfred-server restart, every main Claude that was hosting a
     pending subagent has died, so the subagents are gone too.
     `Loader.Load` leaves `Subagents` as the empty map. Any historical
     SubagentStart/Stop events in the .jsonl are not used to seed it.

**Rebuild cost model.** RebuildBgTasksFromJsonlAndTmux runs **exactly
once per session per alfred-server lifetime**, inside Loader.Load. It
does NOT run on every `/claude-state` request — `SessionManager.
GetOrLoad`'s in-memory cache + singleflight guard means subsequent
hydrate requests for the same session return the same in-memory
`SessionState.BgTasks`, mutated only by live WS events (`task_started`,
`task_notification`, `task_updated`, `bg_task_stopped`). Page refreshes,
WS reconnects, and concurrent client opens all hit the cached state.
   ↓
WS clients reconnect (existing wsEpoch path) → /claude-state hydrate
returns the rebuilt BgTasks. Refresh parity holds.
```

The BgTasks field itself is **still not in snapshot.json**. The wire
shape and JSON tag direction stay as in the refresh-parity spec. The
only invariant change is the doc comment: from "not persisted" to "not
persisted in snapshot.json — re-derivable on restart from .jsonl +
tmux probe".

## Frontend

### Header badge

A new `workspace__header-right` button, immediately left of the
sidebar-toggle icon:

```
[⚙ N Background tasks]   (N = bgTasks in_progress + subagents running)
                          (N=0 → disabled, gray)
```

While `claude.turnsLoaded === false` (the existing hydrate gate from
the refresh-parity spec), the badge renders as `⚙ …` and is
non-interactive; the panel, if open, shows a `Loading…` placeholder
instead of an empty list. This prevents the visible flicker of "I had
3 tasks → I have 0 tasks → I have 3 tasks" during the ~100 ms between
React mount and the `/claude-state` response. Once `turnsLoaded`
flips to true (existing `useClaudeStateLoader` machinery), the badge
and panel switch to the live count.

Click toggles the flyout panel. The open state persists **globally**
(not per session) via `localStorage['alfred_bg_tasks_panel_open']`,
matching the pattern of `alfred_right_sidebar_hidden` /
`alfred_left_sidebar_collapsed`. Rationale: per-session keys would
leak forever (no cleanup hook on session delete); global state is
simpler and the user expectation "I closed this panel; it stays
closed" is more important than per-session memory.

### Panel

Floating card anchored below the badge, width ~440px, does not consume
grid space (does not perturb `WorkspacePage` `gridTemplateColumns`).
Contents:

```
┌─ Background tasks ────────────────────────── × ┐
│  Main Claude: ● running / ○ idle / ✗ exited     │
│  Live · 3 running   (or red ⚠ Disconnected)     │
│  ─────────────────────────────────────────────  │
│  Background bash                                │
│    ▸ <description or command preview>           │
│      <toolName> · running · <elapsed>           │
│      [Stop] [View logs]                         │
│      Latest: <lastEventSummary>                 │
│                                                 │
│  Subagents (blocking main Claude)               │
│    ▸ <agentType>                                │
│      Agent · running · <elapsed>                │
│                                                 │
│  ─────────────────────────────────────────────  │
│  Recently finished                              │
│    ▾ <description>                              │
│      <toolName> · ✓ done · <elapsed> · N events │
│      Latest: <lastEventSummary>                 │
│      [expanded: full input / result / logs]     │
│                                                 │
│  Completed tasks hide after 60s.                │
└─────────────────────────────────────────────────┘
```

Stop button posts `{ type: "stop_bg_task", taskId }`. Server resolves
the task's PID via tmux pane introspection (PanePID + child walk) and
sends SIGINT first; SIGKILL after 5s if still alive. The result is a
`bg_task_stopped` frame.

Stop button is enabled only when `bgTask.status === 'in_progress'`.
For tasks rebuilt as `exited` (server-restart recovery), the button
is hidden — there is no process to kill.

View logs button opens an inline `<pre>` showing the tail of
`<dataDir>/sessions/<sid>/claude-ui/bgtasks/<taskId>.log`.

Two candidate capture mechanisms; one is chosen by the spike (see
Spike section, item 2):

- **Preferred — `updatedInput` rewrite at the bridge.** The PreToolUse
  hook response supports an `updatedInput` field per Anthropic's hook
  protocol. When dispatching a `Bash(run_in_background=true)` decision,
  the bridge returns the allow decision along with
  `updatedInput.command = "{ <original-command>; } >> <log-path> 2>&1"`.
  tool_use_id is in the hook payload, so the log path is deterministic.
  Requires confirmation that the installed Claude CLI honors
  `updatedInput` for the Bash tool (spike item 2).

- **Fallback — pipe-pane demux.** If the CLI does not honor
  `updatedInput`, capture the whole claude-ui pane via pipe-pane and
  demultiplex offline by correlating chunks with `task_started`
  events' time windows. Brittle but works as a degraded mode. The
  panel surfaces a one-time warning if this path is active.

Frontend retrieves the log via:

- `GET /api/sessions/{sid}/bg-tasks/{taskId}/log?tail=N` — REST tail
  read, used on panel open and after page reload. Returns up to N
  bytes from end of file.
- `bg_task_stdout_chunk` WS frame — incremental tail while the panel
  is open and the task is running. Reducer appends to a per-task
  ring buffer capped at 64 KB.

This split mirrors shell mode's REST-tail + WS-incremental pattern
(see `/api/.../commands/{id}/output`). Refresh parity: the user
reloads, REST tail re-fetches up to the cap, WS resumes for further
chunks. Nothing has to survive locally.

### Reducer changes

- `claudeReducer.ts` — three coercers (`asTaskStarted`,
  `asTaskNotification`, `asTaskUpdated`) collapse into one
  `asTaskPayload(payload, kind)` discriminated union. Mechanical
  refactor.
- New WS frames: `bg_task_stdout_chunk { taskId, bytes }`,
  `bg_task_stopped { taskId, exitCode, reason }`.
- Reducer handles both, attaching log chunks to a per-task log buffer
  (capped at e.g. 64KB tail) and updating status on stop.

### Hardcoded "Monitor" removal

`ClaudeChatView.tsx::TurnStatsLine` currently filters
`tool.name === 'Monitor'` and gates on `turn.done`. Both go: stats line
counts every block with `bgTaskId`, regardless of tool name, and
renders mid-turn too.

A new constant `BG_TASK_TOOL_NAMES` in `types.ts` retains the *set*
("Bash", "Monitor") for cosmetic differentiation in the panel (chip
text), but never gates visibility.

## Refresh parity audit

Every piece of state visible in the panel is enumerated here with its
truth source and its survival behavior across page refresh and
alfred-server restart. This is the contract the implementation must
preserve.

| State | Truth source | Page refresh (F5) | alfred-server restart |
|---|---|---|---|
| `bgTasks[*]` map | `claudestate.SessionState.BgTasks` in alfred-server memory | `/claude-state` REST hydrate ✅ | Rebuilt by `Loader` (jsonl replay ∩ tmux probe) ✅ |
| `bgTasks[*].status` | live WS events; rebuild for restart | live → stays live; cached → REST returns it ✅ | `in_progress` if PID found, `exited` if not ✅ |
| `bgTasks[*].lastEventSummary` | `task_notification` WS events; rebuild from jsonl on restart | REST returns whatever is in memory ✅ | replayed from .jsonl up to last persisted event ✅ |
| `bgTasks[*].notificationCount` | live counter; rebuild = count of `task_notification` events in jsonl | REST returns counter ✅ | replayed (count) ✅ |
| `bgTasks[*]` log buffer (View logs panel) | log file on disk (mechanism per spike 2) | `GET /api/sessions/{sid}/bg-tasks/{taskId}/log?tail=N` ✅ | file survives in tmux's filesystem ✅ |
| `subagents[*]` map | `claudestate.SessionState.Subagents` in memory | REST hydrate ✅ | **NOT rebuilt** — main Claude died with the process; subagents necessarily ended too. Empty after restart by design |
| `inFlight` | derived from trailing turn's `done` | REST hydrate ✅ | `finalizeStaleTrailingTurn` flips it (invariant #4) ✅ |
| Panel open/closed | `localStorage['alfred_bg_tasks_panel_open']` (global) | localStorage read ✅ | unaffected ✅ |
| Expanded task rows (which `▸`/`▾` is open) | per-tab `useState`, ephemeral | resets on refresh — accepted (small surface, low value to persist) | unaffected |
| 60s "Recently finished" fade | derived from `Date.now() - bgTask.finishedAt` | naturally refresh-safe ✅ | naturally refresh-safe ✅ |
| Badge count N | derived from `bgTasks` + `subagents` | follows hydrate ✅ | follows hydrate ✅ |
| Loading-shimmer state (`turnsLoaded === false`) | local flag from `useClaudeStateLoader` | resets on mount → shimmer until /claude-state returns → real data | same |

The single deliberate parity loss is "which rows are currently
expanded inside the panel". Persisting it across refresh is possible
but cheap-to-skip; if a user complains, encode it as
`localStorage['alfred_bg_tasks_expanded:<sid>']` later. Everything
else holds.

## Spike (pre-implementation)

Three independent technical unknowns must be answered before the
implementation plan is written. Each is a short manual experiment
(~15 min total). Results are recorded as a decision note alongside
the plan.

### Spike 1 — SIGHUP-safe wrapping for claude -p inside a tmux pane

Determine the exact send-keys wrapping needed for background bashes
to survive `claude -p`'s exit.

1. `tmux new-session -d -s spike-bg`
2. `tmux send-keys -t spike-bg 'claude -p "Use Bash run_in_background=true to start: sleep 60 && echo done. Then exit." --output-format stream-json --include-hook-events --include-partial-messages --verbose --dangerously-skip-permissions' Enter`
3. Wait for claude -p to exit (visible in pane output).
4. `ps -ef | grep sleep` — is the sleep still running? what's its PPID?
5. If sleep was killed, re-run with wrapping options in this order:
   `nohup claude -p ... &`, `setsid claude -p ...`,
   `setsid bash -c '...'`.
6. Pick the smallest wrapping that survives. Record the choice.

If the spike disproves the need for wrapping, `StartPrompt` simplifies
by one step.

### Spike 2 — Does the CLI honor `updatedInput` for Bash?

Determines whether the preferred log-capture mechanism works
(see Frontend → Panel → View logs).

1. Edit `~/.claude/settings.json` PreToolUse hook to point at a tiny
   shell script that prints
   `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"command":"echo ALFRED_REWRITTEN; sleep 1"}}}`
   when matched on Bash.
2. `claude -p "Run Bash: echo ORIGINAL"`
3. Observe stream-json: is the actually-executed command `echo
   ORIGINAL` or `echo ALFRED_REWRITTEN`?
4. If rewritten → preferred path works. If not → fall back to
   pipe-pane demux (see View logs section).

### Spike 3 — Does pipe-pane survive alfred-server restart?

Confirms the assumption that the prior pipe-pane writer dies with
alfred-server and must be re-issued by ResumeOpenTurns.

1. `tmux new-session -d -s spike-pipe`
2. Start a writer process whose stdout is piped to tmux: `tmux
   pipe-pane -t spike-pipe -o "cat > /tmp/spike-pipe.log"`
3. In the pane, `tmux send-keys -t spike-pipe 'echo A' Enter`. Confirm
   `/tmp/spike-pipe.log` contains `A`.
4. `tmux send-keys -t spike-pipe 'echo B' Enter`. Confirm log contains
   `B`.
5. `kill -KILL` the `cat` writer process.
6. `tmux send-keys -t spike-pipe 'echo C' Enter`. Does the log contain
   `C` or stop at `B`? (Expected: stops at B.) Confirms re-issue is
   required.

## Error handling

| Failure | Behavior |
|---|---|
| `tmux new-window` fails (e.g., tmux server crashed mid-startup) | Surface as `claude_error { code: "claude_ui_window_unavailable" }`; existing dispatchClaudeError finalizes the optimistic turn via Apply (invariant #4) |
| Sentinel never observed (claude -p hung, no exit) | Existing `finalizeStaleTrailingTurn` covers it on next server boot. Live path: existing reaper still SIGINTs via `stopRun` on user-initiated interrupt |
| StreamReader can't open jsonl (write race) | Retry once with backoff; if still failing, log WARN and continue — the line will be re-read on next iteration |
| Stop button: pane PID resolves to nothing | Already exited; treat as success, emit `bg_task_stopped { reason: "already_exited" }` |
| View logs button: log file missing | Emit `bg_task_stopped { reason: "log_unavailable" }`; frontend shows "No log captured" |
| Two simultaneous prompts to same session | Existing single-flight gate (`Manager.GetInFlightTurnID`) rejects the second; frontend sees `claude_error` |

## Testing

| Layer | Tests |
|---|---|
| Unit (Go) | `claudeuiwindow.{EnsureWindow,StartPrompt,KillBgTask,ReadLogs}`; sentinel parser; path helpers |
| Unit (Go) | `claudestate.Loader.RebuildBgTasksFromJsonlAndTmux` — golden jsonl + mock tmux probe |
| Unit (TS) | `asTaskPayload(kind)` for each kind; reducer for `bg_task_stdout_chunk`, `bg_task_stopped` |
| Component (TS) | `BackgroundTasksPanel.test.tsx` — N counting, empty state, Bash without Monitor name renders, localStorage open/close persistence, 60s fade uses date diff |
| Integration (Go, env-gated `ALFRED_E2E_CLAUDE=1`) | spawn real tmux + real claude -p; assert stream-json reaches Apply; assert `Bash(run_in_background)` produces task_started in BgTasks |
| Recovery (Go) | start prompt → mid-stream SIGKILL alfred-server → restart → assert (a) trailing turn finalized stale (b) BgTasks rebuilt from jsonl + tmux probe matches actual surviving processes |
| Invariant guards (Go) | `TestBgTasks_StillTransientInSnapshot` (snapshot.json must NOT contain BgTasks); `TestClaudeUIWindow_LazyCreate` (no claude-ui window until first prompt); `TestStaleTrailingTurn_OnRestart_FinalizesUI` |
| E2E (Playwright) | `web/e2e/regression.spec.ts` adds: UI mode prompt → starts background bash → page reload → assert panel still shows it, Live, status correct |

## CONTEXT.md changes

1. **Four invariants → Five invariants** — add #5:

   > **UI mode's `claude -p` runs inside the alfred session's tmux
   > session, in a `claude-ui` window. Background bashes forked by
   > Claude inherit that window. alfred-server is the observer of the
   > window, not the parent process of the claude. The tmux session is
   > the ownership anchor: alfred-server restart does not disturb
   > in-flight work; the window and its processes survive. Only tmux
   > server death (pod restart) tears them down.**

2. **Non-obvious traps table** — two new rows:

   > | Trap | What happens if you forget | Test |
   > | Background bashes run in `claude-ui` window, parented to that window's bash, NOT to alfred-server. SIGHUP-safety on `claude -p` exit depends on the spike-determined wrapping (`nohup` / `setsid`). | Drop the wrapper and your bg tasks die the instant `claude -p` exits; chat looks fine, ps shows nothing | `TestE2E_BgTaskSurvivesClaudePExit` |
   > | tmux `pipe-pane` stops writing the moment alfred-server (the writer process) dies. Restart-time recovery MUST re-issue `pipe-pane` for every `claude-ui` window before any new sentinel or bg-task stdout can be observed. The pane's output continues during the gap and is silently lost. | After alfred-server restart, `__ALFRED_DONE` sentinels never fire (turns hang forever) and bg-task log viewer shows truncated tails ending at the restart instant | `TestResumeOpenTurns_ReissuesPipePaneBeforeStreamReader` |

3. **Why these choices table** — new row:

   > | UI mode runs `claude -p` inside tmux (not direct `exec.Command`) | `exec.Command` was simpler but coupled the background bash's lifetime to a transient parent (chasing trap 1) AND lost everything on alfred-server restart (trap 3). tmux-resident `claude -p` reuses shell mode's well-tested StreamReader offset recovery, makes the tmux session the ownership anchor, and means alfred restarts are invisible to in-flight work. |

4. **Quick orientation table** — rewrite rows 196 and 198:

   - `Claude UI mode (ChatGPT-style rendered chat)` — change "Backend:
     `internal/claude/{runner,parser,event,bridge,dispatcher,settings}.go`
     — forks `claude -p` per prompt" to "Backend:
     `internal/claudeuiwindow/`,
     `internal/claude/{parser,event,bridge,dispatcher,settings}.go` —
     `claudeuiwindow.StartPrompt` launches `claude -p` in the alfred
     session's tmux `claude-ui` window via `send-keys`, then watches
     the per-turn `.jsonl` via StreamReader. `runner.go` is gone."

5. **`internal/claudestate/types.go::BgTask` doc comment** — change

   ```
   // BgTask tracks one CLI background task (Monitor's detached bash).
   // External-resource reference — not persisted.
   ```

   to

   ```
   // BgTask tracks one Claude-CLI-spawned background task. Producers:
   // Monitor's detached bash, Bash(run_in_background=true), future
   // CLI background-emitting tools. External-resource reference —
   // not persisted in snapshot.json, re-derived on alfred-server
   // restart from the per-turn .jsonl files plus a tmux pane probe
   // of the claude-ui window. See spec 2026-06-19.
   ```

## Migration / rollout

Single-pod deployment. No feature flag. Rollout sequence:

1. PR1 (low-risk refactor, no behavior change): collapse three TS task
   coercers into `asTaskPayload(kind)`; update BgTask doc comments;
   add `BG_TASK_TOOL_NAMES` constant. Ship to main.
2. Spike (≤ 1 day): manual tmux + claude -p experiment to fix the
   SIGHUP-wrapping question. Result captured as a one-line decision
   note in the implementation plan.
3. PR2 (backend, behind no flag): `internal/claudeuiwindow/` new
   package; rewire `handleClaudePrompt` to use it; old `runner.go`
   stays in tree for one release as a fallback if the
   `ALFRED_LEGACY_CLAUDE_RUNNER` env var is set (the variable exists
   only during this transition; removed in PR4).
4. PR3 (frontend): `BackgroundTasksPanel`, header badge, `TurnStatsLine`
   un-hardcode, new reducer cases.
5. PR4 (cleanup, one release later): remove `runner.go` and the
   `ALFRED_LEGACY_CLAUDE_RUNNER` escape hatch. Remove the doc rows that
   reference the legacy path.

## Out of scope (future work)

- TUI mode background visibility (would require parsing Claude's TUI
  output bytes; explicitly punted in this spec).
- Cross-pod-restart durability (would require tmux to outlive the pod,
  i.e., run tmux outside the container).
- A real "task manager" UX (priorities, queueing, persistent task
  log archive). The panel here is a live observability surface, not a
  task store.
