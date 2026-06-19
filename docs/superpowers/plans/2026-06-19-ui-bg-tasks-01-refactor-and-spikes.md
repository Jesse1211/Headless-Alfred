# UI Background Tasks — Plan 1: Refactor + Spikes

> ⚠️ **SUPERSEDED 2026-06-19** — DO NOT USE.
>
> A `claude -p` spike on 2026-06-19 confirmed Claude CLI is the sole
> owner of bg-task lifecycle (it SIGKILLs all in-flight tasks on
> turn end). The tmux-containerization + SIGHUP-wrapping +
> updatedInput-rewriting + pipe-pane-demux mechanics this plan
> orchestrated all become moot — see SUPERSEDED header in
> `docs/superpowers/specs/2026-06-19-ui-mode-background-tasks-design.md`.
>
> The canonical execution plan is now **`DESIGN.md` at repo root**
> (feature-factory format, 16 tasks). Plan 1's Part A (refactor)
> survived; Part B (the three spikes) was cancelled.
>
> Kept for archaeological context only. Do not implement from this
> file.

---

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the low-risk frontend refactor that unblocks the upcoming UI background-tasks panel, and execute the three pre-implementation spikes whose answers shape Plan 2 (the backend rewrite).

**Architecture:** Two parts, independent:
- Part A (Tasks 1–4): Pure TypeScript refactor. Collapse three near-identical task-payload coercers into one discriminated-union helper. Update three doc comments. Add a constant. Zero behavior change.
- Part B (Task 5): Three manual spikes — SIGHUP-safety of `claude -p` inside tmux, whether Claude CLI honors PreToolUse `updatedInput`, and whether tmux `pipe-pane` survives the writer dying. Document each result inline in a markdown decision note.

**Tech Stack:** TypeScript + Vitest (frontend). Manual tmux + Claude CLI (spikes). No Go changes in this plan.

**Spec:** `docs/superpowers/specs/2026-06-19-ui-mode-background-tasks-design.md`. This plan implements R2/R3 from the spec's "Migration / rollout" section and the three "Spike (pre-implementation)" experiments.

---

## File Structure

| Action | Path | Responsibility |
|---|---|---|
| Modify | `web/src/features/sessions/claudeReducer.ts` | Replace three coercers with one `asTaskPayload(kind, payload)` discriminated-union helper; rewire three switch arms |
| Modify | `web/src/features/sessions/claudeReducer.test.ts` | Existing tests stay green; add one test driving `asTaskPayload` directly |
| Modify | `web/src/features/sessions/types.ts` | Update `BgTask` and `ClaudeToolCall.bgTaskId` doc comments; add `BG_TASK_TOOL_NAMES` constant |
| Create | `docs/superpowers/plans/2026-06-19-ui-bg-tasks-spike-notes.md` | Append-only decision notes from the three spikes |

`internal/claudestate/types.go` keeps the existing BgTask Go doc comment for now — the Go-side doc update lands in Plan 2 alongside the rebuild path, because the Go comment will reference a path (`internal/claudeuiwindow/`) that doesn't exist yet in Plan 1.

---

## Part A — Frontend refactor

The three coercers (`asTaskStarted`, `asTaskNotification`, `asTaskUpdated`) in `claudeReducer.ts:628-674` are near-identical snake-case → camel-case shims that diverge only in fields. Collapse them so Plan 2's potential future events (e.g., `task_stopped` from a Stop button) cost one switch arm instead of seven.

### Task 1: Add `asTaskPayload` and replace `asTaskStarted` callers

**Files:**
- Modify: `web/src/features/sessions/claudeReducer.ts:338-359` (the `task_started` switch arm)
- Modify: `web/src/features/sessions/claudeReducer.ts:628-643` (delete `asTaskStarted`)
- Modify: `web/src/features/sessions/claudeReducer.ts` — add `asTaskPayload` near the other `as*` helpers (alphabetic locality, immediately above `asTaskStarted`'s former position around line 627)
- Test: `web/src/features/sessions/claudeReducer.test.ts` (existing — must stay green)

- [ ] **Step 1: Verify existing tests pass against unchanged code**

Run: `cd web && npx vitest run src/features/sessions/claudeReducer.test.ts`
Expected: All tests pass (this is the baseline before refactoring).

- [ ] **Step 2: Add `asTaskPayload` helper above the existing task coercers**

Insert this block in `web/src/features/sessions/claudeReducer.ts` immediately above the `function asTaskStarted` declaration (around line 627):

```ts
// Discriminated coercer for the three task-lifecycle stream-json
// payloads. The wire format is snake_case (from internal/claude
// stream-json); the reducer expects camelCase. Each kind maps to a
// distinct shape, so the return type is a tagged union and the
// caller switches on `.kind` to access the right fields. Adding a
// future `task_*` event is one new case here + one new reducer arm
// + one new test, instead of a whole new coercer.
type TaskPayloadStarted = {
  kind: 'task_started'
  taskId: string
  toolUseId: string
  description: string
  taskType: string
}
type TaskPayloadNotification = {
  kind: 'task_notification'
  taskId: string
  toolUseId: string
  status: string
  summary: string
}
type TaskPayloadUpdated = {
  kind: 'task_updated'
  taskId: string
  status: string
  endTime: number
}
type TaskPayload =
  | TaskPayloadStarted
  | TaskPayloadNotification
  | TaskPayloadUpdated

function asTaskPayload(
  kind: 'task_started',
  payload: unknown,
): TaskPayloadStarted
function asTaskPayload(
  kind: 'task_notification',
  payload: unknown,
): TaskPayloadNotification
function asTaskPayload(
  kind: 'task_updated',
  payload: unknown,
): TaskPayloadUpdated
function asTaskPayload(
  kind: TaskPayload['kind'],
  payload: unknown,
): TaskPayload {
  const p = (payload ?? {}) as Record<string, unknown>
  if (kind === 'task_started') {
    return {
      kind,
      taskId: (p.task_id as string) ?? '',
      toolUseId: (p.tool_use_id as string) ?? '',
      description: (p.description as string) ?? '',
      taskType: (p.task_type as string) ?? '',
    }
  }
  if (kind === 'task_notification') {
    return {
      kind,
      taskId: (p.task_id as string) ?? '',
      toolUseId: (p.tool_use_id as string) ?? '',
      status: (p.status as string) ?? '',
      summary: (p.summary as string) ?? '',
    }
  }
  // task_updated
  const patch = (p.patch as Record<string, unknown> | undefined) ?? {}
  return {
    kind,
    taskId: (p.task_id as string) ?? '',
    status: (patch.status as string) ?? '',
    endTime: (patch.end_time as number) ?? 0,
  }
}
```

- [ ] **Step 3: Rewire the `task_started` switch arm**

Replace the body of `case 'task_started':` (around line 338) with this:

```ts
    case 'task_started': {
      const p = asTaskPayload('task_started', payload)
      if (!p.taskId) return prev
      const bgTasks = {
        ...prev.bgTasks,
        [p.taskId]: {
          taskId: p.taskId,
          toolUseId: p.toolUseId,
          description: p.description,
          taskType: p.taskType,
          startedAt: asTimestamp(frameTs),
          status: 'in_progress' as const,
          notificationCount: 0,
        },
      }
      const linkedTurns = prev.turns.map((t) => ({
        ...t,
        blocks: patchToolBlock(t.blocks, p.toolUseId, (tool) => ({ ...tool, bgTaskId: p.taskId })),
      }))
      return { ...prev, bgTasks, turns: linkedTurns }
    }
```

Note: the only difference from the original is `asTaskStarted(payload)` → `asTaskPayload('task_started', payload)`. The comment about Monitor card linkage is removed because Plan 2 generalises this; the behavior is unchanged.

- [ ] **Step 4: Delete the old `asTaskStarted` function**

Delete the entire `function asTaskStarted(...)` block (around line 628-643). `asTaskNotification` and `asTaskUpdated` stay for now — Tasks 2 and 3 retire them.

- [ ] **Step 5: Run tests, expect them to still pass**

Run: `cd web && npx vitest run src/features/sessions/claudeReducer.test.ts`
Expected: All tests pass. No behavior changed; the test suite already covers `task_started` (`claudeReducer.test.ts:56` onwards).

- [ ] **Step 6: Run typecheck**

Run: `cd web && npx tsc --noEmit`
Expected: No errors.

- [ ] **Step 7: Commit**

```bash
git add web/src/features/sessions/claudeReducer.ts
git commit -m "refactor(claudeReducer): collapse asTaskStarted into asTaskPayload union

First of three coercers. asTaskNotification and asTaskUpdated land
next; behavior unchanged.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

### Task 2: Replace `asTaskNotification` caller

**Files:**
- Modify: `web/src/features/sessions/claudeReducer.ts` — `task_notification` switch arm + delete `asTaskNotification`
- Test: `web/src/features/sessions/claudeReducer.test.ts` (existing — must stay green)

- [ ] **Step 1: Rewire the `task_notification` switch arm**

Replace `case 'task_notification':` body with:

```ts
    case 'task_notification': {
      const p = asTaskPayload('task_notification', payload)
      if (!p.taskId || !prev.bgTasks[p.taskId]) return prev
      const cur = prev.bgTasks[p.taskId]
      const bgTasks = {
        ...prev.bgTasks,
        [p.taskId]: {
          ...cur,
          notificationCount: cur.notificationCount + 1,
          lastEventSummary: p.summary,
          status: p.status === 'completed' ? 'completed' as const : cur.status,
          finishedAt: p.status === 'completed' && !cur.finishedAt
            ? asTimestamp(frameTs)
            : cur.finishedAt,
        },
      }
      return { ...prev, bgTasks }
    }
```

Only change: `asTaskNotification(payload)` → `asTaskPayload('task_notification', payload)`. The comment about "Some CLIs emit the final 'completed' status…" stays.

- [ ] **Step 2: Delete the old `asTaskNotification` function**

Delete the entire `function asTaskNotification(...)` block (around the former lines 645-660).

- [ ] **Step 3: Run tests + typecheck**

Run: `cd web && npx vitest run src/features/sessions/claudeReducer.test.ts && npx tsc --noEmit`
Expected: All tests pass, no type errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/claudeReducer.ts
git commit -m "refactor(claudeReducer): collapse asTaskNotification into asTaskPayload

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

### Task 3: Replace `asTaskUpdated` caller

**Files:**
- Modify: `web/src/features/sessions/claudeReducer.ts` — `task_updated` switch arm + delete `asTaskUpdated`

- [ ] **Step 1: Rewire the `task_updated` switch arm**

Replace `case 'task_updated':` body with:

```ts
    case 'task_updated': {
      const p = asTaskPayload('task_updated', payload)
      if (!p.taskId || !prev.bgTasks[p.taskId]) return prev
      const cur = prev.bgTasks[p.taskId]
      if (p.status !== 'completed' && p.status !== 'failed') return prev
      const status: 'completed' | 'failed' = p.status
      const bgTasks = {
        ...prev.bgTasks,
        [p.taskId]: {
          ...cur,
          status,
          finishedAt: p.endTime
            ? new Date(p.endTime).toISOString()
            : asTimestamp(frameTs),
        },
      }
      return { ...prev, bgTasks }
    }
```

Only change: `asTaskUpdated(payload)` → `asTaskPayload('task_updated', payload)`.

- [ ] **Step 2: Delete the old `asTaskUpdated` function**

Delete the entire `function asTaskUpdated(...)` block.

- [ ] **Step 3: Run tests + typecheck**

Run: `cd web && npx vitest run src/features/sessions/claudeReducer.test.ts && npx tsc --noEmit`
Expected: All tests pass, no type errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/claudeReducer.ts
git commit -m "refactor(claudeReducer): collapse asTaskUpdated into asTaskPayload

Three task coercers are now one. Future task-lifecycle events drop
from 6 sites of change to 4 (constant + reducer + test + WS type).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

### Task 4: Update types.ts doc comments + add BG_TASK_TOOL_NAMES + drive helper with direct test

**Files:**
- Modify: `web/src/features/sessions/types.ts` (around line 105 — `bgTaskId` comment; around line 110 — `BgTask` interface comment; append new export `BG_TASK_TOOL_NAMES` near related declarations)
- Modify: `web/src/features/sessions/claudeReducer.test.ts` — add one test driving `asTaskPayload` directly

- [ ] **Step 1: Add the new test (TDD: write first, expect to compile but red because the production code doesn't expose `asTaskPayload`)**

Append to `web/src/features/sessions/claudeReducer.test.ts` (anywhere among the existing describes; if uncertain, place after the `task_updated` tests):

```ts
describe('asTaskPayload discriminated union', () => {
  // We test asTaskPayload indirectly: drive each switch arm through
  // applyClaudeEvent and assert on observable state. Direct export
  // of the helper is avoided to keep reducer internals private.
  it('task_started decodes snake_case wire to camelCase state', () => {
    let s = makeInitial()
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'wire_task',
      tool_use_id: 'tu_wire',
      description: 'from wire',
      task_type: 'local_bash',
    } as unknown, TS, 't_wire')
    expect(s.bgTasks['wire_task']).toMatchObject({
      taskId: 'wire_task',
      toolUseId: 'tu_wire',
      description: 'from wire',
      taskType: 'local_bash',
    })
  })

  it('task_notification preserves snake_case → camelCase status mapping', () => {
    let s = makeInitial()
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'wire_task',
      tool_use_id: 'tu_wire',
      description: '',
      task_type: 'local_bash',
    } as unknown, TS, 't_wire')
    s = applyClaudeEvent(s, 'task_notification', {
      task_id: 'wire_task',
      tool_use_id: 'tu_wire',
      status: 'completed',
      summary: 'wire summary',
    } as unknown, TS2, 't_wire')
    expect(s.bgTasks['wire_task'].lastEventSummary).toBe('wire summary')
    expect(s.bgTasks['wire_task'].status).toBe('completed')
  })

  it('task_updated decodes nested patch.end_time', () => {
    let s = makeInitial()
    s = applyClaudeEvent(s, 'task_started', {
      task_id: 'wire_task',
      tool_use_id: 'tu_wire',
      description: '',
      task_type: 'local_bash',
    } as unknown, TS, 't_wire')
    s = applyClaudeEvent(s, 'task_updated', {
      task_id: 'wire_task',
      patch: { status: 'completed', end_time: 1781706910801 },
    } as unknown, TS2, 't_wire')
    expect(s.bgTasks['wire_task'].finishedAt).toBe(
      new Date(1781706910801).toISOString(),
    )
  })
})
```

If `TS`, `TS2`, and `makeInitial` are not in scope at the chosen insertion point, copy them from the existing test file's top — they are defined as shared fixtures near line 1-30.

- [ ] **Step 2: Run the new test, expect green**

Run: `cd web && npx vitest run src/features/sessions/claudeReducer.test.ts -t "asTaskPayload"`
Expected: All three new tests pass (the refactor in Tasks 1-3 already routes through `asTaskPayload`).

- [ ] **Step 3: Update `BgTask` doc comment in `types.ts`**

Find the existing block around line 110:

```ts
// BgTask tracks one CLI-managed background task (today: Monitor's
// detached bash process). Created from task_started, updated by
// task_notification, terminated by task_updated.status=completed.
```

Replace with:

```ts
// BgTask tracks one Claude-CLI-spawned background task. Producers
// observed in the wild: Monitor's detached bash, and
// Bash(run_in_background=true). The task lifecycle is uniform across
// producers (task_started → task_notification* → task_updated), so
// BgTask is producer-agnostic. The CLI sets taskType (e.g. "local_bash")
// if the source matters downstream.
//
// External-resource reference — not persisted in snapshot.json. On
// alfred-server restart, BgTasks is re-derived by Loader from .jsonl
// replay plus a tmux probe of the claude-ui window. See spec
// 2026-06-19-ui-mode-background-tasks-design.md.
```

- [ ] **Step 4: Update `bgTaskId` doc comment**

Find the existing comment around line 105:

```ts
  // For Monitor: the CLI's background task id, set when a matching
  // task_started event arrives. Links this tool block to bgTasks[bgTaskId].
  bgTaskId?: string
```

Replace with:

```ts
  // CLI background-task id, set when a matching task_started event
  // arrives. Links this tool block to bgTasks[bgTaskId]. Set for any
  // tool that spawns a background task (Monitor, Bash run_in_background).
  bgTaskId?: string
```

- [ ] **Step 5: Add `BG_TASK_TOOL_NAMES` constant**

In the same file, find a suitable location (immediately above `export interface BgTask` is fine). Add:

```ts
// Tool names whose tool_use events are known to also emit a matching
// task_started event. Used as a hint for cosmetic differentiation in
// the background-tasks panel chip text; NEVER used to gate visibility
// (see ClaudeChatView.tsx historical Monitor-hardcoding regression).
// Add new producers here when the CLI gains them.
export const BG_TASK_TOOL_NAMES = ['Monitor', 'Bash'] as const
export type BgTaskToolName = (typeof BG_TASK_TOOL_NAMES)[number]
```

- [ ] **Step 6: Run typecheck**

Run: `cd web && npx tsc --noEmit`
Expected: No errors.

- [ ] **Step 7: Run the full reducer test file**

Run: `cd web && npx vitest run src/features/sessions/claudeReducer.test.ts`
Expected: All tests pass, including the three new ones from Step 1.

- [ ] **Step 8: Commit**

```bash
git add web/src/features/sessions/claudeReducer.test.ts web/src/features/sessions/types.ts
git commit -m "refactor(types): update BgTask doc comments + add BG_TASK_TOOL_NAMES

Removes the Monitor-only framing in BgTask + bgTaskId docs (the
field has been multi-producer in practice for a while). Adds a
constant for the panel's chip text. No behavior change.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Part B — Spikes

The three spikes from the spec's "Spike (pre-implementation)" section. Each is a ~5 minute manual experiment whose result becomes a one-line decision committed to a notes file. Plan 2 reads that file to choose its wrapping / fallback paths.

### Task 5: Run the three spikes and record results

**Files:**
- Create: `docs/superpowers/plans/2026-06-19-ui-bg-tasks-spike-notes.md`

This task is NOT TDD — it is a manual exploration whose output is a markdown decision note. Each spike is its own checkbox.

- [ ] **Step 1: Create the notes file with the required structure**

Write `docs/superpowers/plans/2026-06-19-ui-bg-tasks-spike-notes.md`:

```markdown
# UI BG Tasks — Spike Decision Notes

**Date executed:** YYYY-MM-DD (fill in)
**Executor:** (fill in)
**Spec reference:** docs/superpowers/specs/2026-06-19-ui-mode-background-tasks-design.md (Spike section)

These three spikes determine choices for Plan 2 (backend tmux
container) and Plan 3 (frontend log viewer). Each result is a single
sentence; Plan 2's tasks read this file at execution time.

---

## Spike 1: SIGHUP-safe wrapping for claude -p inside tmux

**Question:** Does a background bash forked by `claude -p` survive
`claude -p`'s own exit, when `claude -p` was invoked via tmux send-keys?
If not, which wrapper (`nohup` / `setsid bash -c` / `setsid`) is the
smallest change that lets the bg bash survive?

**Steps:**
1. `tmux kill-session -t spike-bg 2>/dev/null; tmux new-session -d -s spike-bg`
2. `tmux send-keys -t spike-bg 'claude -p "Use Bash run_in_background=true to start: sleep 60 && echo done. Then say done and exit." --output-format stream-json --include-hook-events --include-partial-messages --verbose --dangerously-skip-permissions < /dev/null' Enter`
3. Wait for claude -p to exit (~10s after main Claude says done). Capture pane:
   `tmux capture-pane -t spike-bg -p | tail -30`
4. `ps -ef | grep "sleep 60" | grep -v grep`
5. Note: is the sleep still running? what's its PPID? is the PPID 1 (init / launchd) or some surviving shell?
6. If sleep was killed, re-run from step 1 with these progressively heavier wrappings (replace step 2 with each):
   a. `tmux send-keys -t spike-bg 'nohup claude -p "..." > /tmp/spike.log 2>&1 < /dev/null &' Enter` then `tmux send-keys -t spike-bg 'wait $!' Enter`
   b. `tmux send-keys -t spike-bg 'setsid claude -p "..." < /dev/null' Enter`
   c. `tmux send-keys -t spike-bg 'setsid bash -c "claude -p ... < /dev/null"' Enter`

**Result (fill in after running):**
- Survival without wrapper: YES / NO
- If NO, smallest working wrapper: _____________________
- Wrapping to bake into claudeuiwindow.StartPrompt: _____________________

---

## Spike 2: Does Claude CLI honor PreToolUse `updatedInput` for Bash?

**Question:** When the PreToolUse hook returns a JSON response with
`hookSpecificOutput.updatedInput.command`, does the installed CLI
actually execute the rewritten command, or does it ignore the field
and run the original?

**Steps:**
1. Back up existing hook setting:
   `cp ~/.claude/settings.json ~/.claude/settings.json.bak`
2. Create a probe hook script. Write `/tmp/spike-hook.sh`:
   ```sh
   #!/bin/sh
   tool_name=$(jq -r .tool_name)
   if [ "$tool_name" = "Bash" ]; then
     printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"command":"echo ALFRED_REWRITTEN"}}}'
   else
     printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
   fi
   ```
   `chmod +x /tmp/spike-hook.sh`
3. Edit `~/.claude/settings.json` to point PreToolUse at this script
   (matcher `.*`, command `/tmp/spike-hook.sh`).
4. `claude -p "Run a Bash command: echo ORIGINAL. Then exit." --output-format stream-json --include-hook-events --include-partial-messages --verbose --dangerously-skip-permissions < /dev/null | tee /tmp/spike-stream.log`
5. Inspect the stream:
   `grep -E 'tool_result|stdout' /tmp/spike-stream.log | head -5`
6. Look for "ALFRED_REWRITTEN" or "ORIGINAL" in the tool_result.
7. Restore: `mv ~/.claude/settings.json.bak ~/.claude/settings.json`

**Result (fill in):**
- Output contained: ALFRED_REWRITTEN / ORIGINAL
- updatedInput is honored: YES / NO
- Decision for Plan 3 log-capture: PREFERRED (rewrite at bridge) / FALLBACK (pipe-pane demux)

---

## Spike 3: Does tmux pipe-pane survive its writer process dying?

**Question:** When the process consuming `tmux pipe-pane`'s output
exits, does tmux stop piping (forcing ResumeOpenTurns to re-issue
pipe-pane after alfred-server restart) or keep buffering (in which
case we just continue)?

**Steps:**
1. `tmux kill-session -t spike-pipe 2>/dev/null; tmux new-session -d -s spike-pipe`
2. Start a long-lived writer:
   `(tail -f /dev/null > /tmp/spike-pipe.fifo) &`
   `WRITER_PID=$!`
   `mkfifo /tmp/spike-pipe.fifo 2>/dev/null; (cat /tmp/spike-pipe.fifo > /tmp/spike-pipe.log) & PIPE_PID=$!`
   Actually simpler:
   `tmux pipe-pane -t spike-pipe -o "cat > /tmp/spike-pipe.log"`
3. `tmux send-keys -t spike-pipe 'echo A' Enter`
4. `sleep 0.5; cat /tmp/spike-pipe.log` — expect `A`.
5. `tmux send-keys -t spike-pipe 'echo B' Enter`
6. `sleep 0.5; cat /tmp/spike-pipe.log` — expect `A\nB`.
7. Find the `cat` writer PID: `ps -ef | grep "cat > /tmp/spike-pipe.log" | grep -v grep | awk '{print $2}'`
8. `kill -KILL <pid>`
9. `tmux send-keys -t spike-pipe 'echo C' Enter`
10. `sleep 0.5; cat /tmp/spike-pipe.log` — does it contain `C` or stop at `B`?

**Result (fill in):**
- After writer death, subsequent pane output reached the log: YES / NO
- Pipe-pane re-issue required after alfred-server restart: YES / NO
- (If YES, this confirms the spec's ResumeOpenTurns step.)

---

## Summary table (fill in after all three spikes)

| Spike | Decision |
|---|---|
| 1 — SIGHUP wrapping | _________________________ |
| 2 — updatedInput honored | YES → preferred / NO → fallback |
| 3 — pipe-pane re-issue required | YES / NO |
```

- [ ] **Step 2: Execute Spike 1 and fill in the result**

Run the steps in the Spike 1 block. Edit the "Result" section of the notes file with the actual outcome. If a wrapper was required, write the exact command string that worked (verbatim, with quoting preserved).

- [ ] **Step 3: Execute Spike 2 and fill in the result**

Run the steps in the Spike 2 block. Edit the "Result" section. Note: if you do not have a working `claude` CLI on the spike machine, mark Spike 2 as `BLOCKED — needs claude CLI`; Plan 2 will then default to the FALLBACK path and Spike 2 reruns later.

- [ ] **Step 4: Execute Spike 3 and fill in the result**

Run the steps in the Spike 3 block. Edit the "Result" section.

- [ ] **Step 5: Fill in the summary table**

Update the bottom table from each spike's individual result.

- [ ] **Step 6: Commit**

```bash
git add docs/superpowers/plans/2026-06-19-ui-bg-tasks-spike-notes.md
git commit -m "docs(spike): record three pre-implementation spike results

UI bg-tasks Plan 1 part B. Spike outcomes drive Plan 2's wrapper
choice (SIGHUP), Plan 3's log-capture choice (updatedInput), and
confirm/refute the pipe-pane re-issue assumption in ResumeOpenTurns.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Done

After Task 5, this plan is complete. Next: Plan 2 (`docs/superpowers/plans/2026-06-19-ui-bg-tasks-02-backend-tmux.md`, not yet written) consumes the spike-notes file when choosing its tmux send-keys wrapping and its log-capture path.
