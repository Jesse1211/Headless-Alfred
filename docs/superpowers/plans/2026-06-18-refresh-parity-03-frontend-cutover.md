# Claude Refresh Parity — Plan 3/3: Frontend Cutover

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Switch the frontend from `/claude-history` to the new `/claude-state` endpoint, strip every local `new Date().toISOString()` out of the reducer so timestamps come from the server-broadcast event payload, add `clientNonce` to the prompt round-trip, and verify refresh-parity end-to-end via Playwright.

**Architecture:** The frontend reducer becomes a pure projection of WS events plus an HTTP hydrate. No persisted field is computed locally. Optimistic placeholder turns get reconciled by `clientNonce` from the `turn_started` broadcast.

**Tech Stack:** TypeScript, React, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-06-18-claude-refresh-parity-design.md`

**Branch:** continue on `refactor/refresh-parity`

**Prerequisite:** Plans 1 & 2 fully merged. The server is producing `claude_event` frames with a `timestamp` field, `turn_started` after `BeginTurn`, `tool_decision_applied` after every decision, and the `GET /api/sessions/{sid}/claude-state` endpoint is live.

---

## File Structure

| Path | Purpose |
|---|---|
| `web/src/lib/api.ts` | New `getClaudeState(sessionID)` returning `ClaudeState`. Old `getClaudeHistory` removed in Task 8. |
| `web/src/features/sessions/types.ts` | TS shape matches the Go `ClaudeState` exactly. Optional time fields become `string \| undefined`. |
| `web/src/features/sessions/useClaudeStateLoader.ts` | Renamed loader hook. Replaces `useClaudeHistoryLoader`. |
| `web/src/features/sessions/useClaudeStateLoader.test.ts` | Hook tests: hydrate success, HTTP error, 404, hydrate-once. |
| `web/src/features/sessions/claudeReducer.ts` | Remove every `new Date().toISOString()`. Time fields read from event payload. Reconcile optimistic turn on `turn_started`. Apply `tool_decision_applied`. |
| `web/src/features/sessions/claudeReducer.test.ts` | Update existing tests to feed explicit `timestamp`. New tests for nonce reconciliation and decision-applied. |
| `web/src/features/sessions/useSessions.ts` | Generate `clientNonce` on send; forward to claude_prompt frame. Wire `useClaudeStateLoader`. |
| `web/src/lib/ws.ts` | Type the new frames if a `ServerMsg` discriminated union lives here. |
| `web/e2e/refresh-parity.spec.ts` | Send a turn → assert visible cost/elapsed/decision → reload → assert same strings. |

---

## Task 1: Rename TS types to match the server snapshot

**Files:**
- Modify: `web/src/features/sessions/types.ts`

- [ ] **Step 1: Open the current `types.ts`**

Verify the current field names and tags. Specifically confirm:

```
grep -n "startedAt\|finishedAt\|totalCostUsd\|bgTaskId" web/src/features/sessions/types.ts
```

- [ ] **Step 2: Update `ClaudeTurn` to optional finishedAt and matching Go shape**

In `types.ts`, change `ClaudeTurn`:

```ts
export interface ClaudeTurn {
  id: string
  prompt: string
  expandedPrompt?: string
  startedAt: string
  // Optional in the wire format: undefined while the turn is still
  // running, set once the server-side reducer fires the result event
  // or finalizeInFlight backstop.
  finishedAt?: string
  blocks: AssistantBlock[]
  thinking?: string[]
  done: boolean
  isError?: boolean
  totalCostUsd?: number
  usage?: {
    inputTokens: number
    outputTokens: number
    cacheReadInputTokens: number
    cacheCreationInputTokens: number
  }

  // Private to the live-streaming reducer; not on the wire. Kept here
  // (rather than on a side map) because each turn's index space is
  // self-contained. Wire serializers ignore these (frontend doesn't
  // serialize ClaudeTurn back to the server; server-side Go has
  // json:"-" tags on the equivalent fields).
  _thinkingIndexMap?: Record<number, number>
  _blockIndexMap?: Record<number, number>
}
```

Update `ClaudeToolCall`:

```ts
export interface ClaudeToolCall {
  toolUseId: string
  name: string
  input?: unknown
  decision: 'allow' | 'deny' | 'pending'
  result?: string
  isError?: boolean
  startedAt?: string
  finishedAt?: string
  bgTaskId?: string
}
```

Update `ClaudeState` to make all top-level slots explicit:

```ts
export interface ClaudeState {
  turns: ClaudeTurn[]
  turnsLoaded?: boolean
  inFlight: boolean
  pending: ClaudeToolApprovalRequest[]
  pendingQuestions: ClaudeQuestionRequest[]
  lastError?: { code: string; message: string }
  bgTasks: Record<string, BgTask>
  subagents: Record<string, SubagentEntry>
}
```

Update `emptyClaudeState`:

```ts
export function emptyClaudeState(): ClaudeState {
  return {
    turns: [],
    inFlight: false,
    pending: [],
    pendingQuestions: [],
    bgTasks: {},
    subagents: {},
  }
}
```

- [ ] **Step 3: Run typecheck**

```
cd web && npx tsc --noEmit 2>&1 | tail -20
```

Expected: errors only in files that read `decision` without the `pending` arm, etc. Track each one and fix it inline:
- Any `tool.decision === 'allow' | 'deny'` should add `'pending'` handling (treat pending as not-yet-decided).

- [ ] **Step 4: Commit**

```
git add web/src/features/sessions/types.ts
git commit -m "refactor(web): ClaudeState types mirror server snapshot exactly

ClaudeTurn.finishedAt and ClaudeToolCall.startedAt/finishedAt are now
optional (string | undefined) to match the Go *time.Time pointers that
omit on the wire. Adds explicit pending decision arm. Index maps
labelled @internal because they live in reducer state but never on the
wire.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 2: New `getClaudeState` API client

**Files:**
- Modify: `web/src/lib/api.ts`

- [ ] **Step 1: Locate `getClaudeHistory`**

```
grep -n "getClaudeHistory" web/src/lib/api.ts
```

- [ ] **Step 2: Add `getClaudeState` alongside (don't remove the old one yet — Task 4 swaps the only caller)**

In `api.ts`:

```ts
import type { ClaudeState } from '../features/sessions/types'

// getClaudeState fetches the server-authoritative ClaudeState for a
// session — full snapshot of in-memory state including turn cost,
// per-tool elapsed, and decisions. Replaces getClaudeHistory; the old
// endpoint stays one release cycle with Deprecation headers for safety.
export async function getClaudeState(sessionID: string): Promise<ClaudeState> {
  const res = await request(`/api/sessions/${encodeURIComponent(sessionID)}/claude-state`)
  return res.json()
}
```

- [ ] **Step 3: Typecheck**

```
cd web && npx tsc --noEmit 2>&1 | tail -10
```

Expected: clean (no new caller yet).

- [ ] **Step 4: Commit**

```
git add web/src/lib/api.ts
git commit -m "feat(web): getClaudeState client matching the new server endpoint

Old getClaudeHistory remains until Task 4 swaps its sole caller, then
Task 8 removes it. Two-step swap keeps the diff small per commit.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 3: New `useClaudeStateLoader` hook

**Files:**
- Create: `web/src/features/sessions/useClaudeStateLoader.ts`
- Create: `web/src/features/sessions/useClaudeStateLoader.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useClaudeStateLoader } from './useClaudeStateLoader'
import { emptyClaudeState, emptyPerSessionState, PerSessionState } from './types'
import * as api from '../../lib/api'

describe('useClaudeStateLoader', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('hydrates the perSession map with server state on first claude entry', async () => {
    vi.spyOn(api, 'getClaudeState').mockResolvedValue({
      ...emptyClaudeState(),
      turns: [{
        id: 'u1', prompt: 'hi', startedAt: '2026-06-18T07:00:00Z',
        blocks: [], done: true,
      }],
      turnsLoaded: true,
    })
    const sid = 'sess1'
    const initial = new Map<string, PerSessionState>()
    initial.set(sid, { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui' })

    const setPerSession = vi.fn()
    renderHook(() =>
      useClaudeStateLoader({
        selectedSessionID: sid,
        perSession: initial,
        setPerSession,
      })
    )
    await waitFor(() => expect(setPerSession).toHaveBeenCalled())

    // Apply the captured updater and inspect the resulting map.
    const updater = setPerSession.mock.calls[0][0]
    const next = updater(initial)
    expect(next.get(sid)!.claude!.turns).toHaveLength(1)
    expect(next.get(sid)!.claude!.turnsLoaded).toBe(true)
  })

  it('skips fetch when turnsLoaded is already true', async () => {
    const fetch = vi.spyOn(api, 'getClaudeState').mockResolvedValue(emptyClaudeState())
    const sid = 'sess1'
    const initial = new Map<string, PerSessionState>()
    initial.set(sid, {
      ...emptyPerSessionState(),
      mode: 'claude', renderer: 'ui',
      claude: { ...emptyClaudeState(), turnsLoaded: true },
    })
    renderHook(() =>
      useClaudeStateLoader({
        selectedSessionID: sid,
        perSession: initial,
        setPerSession: vi.fn(),
      })
    )
    await new Promise((r) => setTimeout(r, 5))
    expect(fetch).not.toHaveBeenCalled()
  })

  it('on HTTP error keeps existing state and sets lastError', async () => {
    vi.spyOn(api, 'getClaudeState').mockRejectedValue(new Error('boom'))
    const sid = 'sess1'
    const existing = {
      ...emptyClaudeState(),
      turns: [{ id: 'kept', prompt: 'kept', startedAt: 'z', blocks: [], done: true }],
    }
    const initial = new Map<string, PerSessionState>()
    initial.set(sid, {
      ...emptyPerSessionState(),
      mode: 'claude', renderer: 'ui', claude: existing,
    })
    const setPerSession = vi.fn()
    renderHook(() =>
      useClaudeStateLoader({
        selectedSessionID: sid,
        perSession: initial,
        setPerSession,
      })
    )
    await waitFor(() => expect(setPerSession).toHaveBeenCalled())
    const updater = setPerSession.mock.calls[0][0]
    const next = updater(initial)
    const c = next.get(sid)!.claude!
    expect(c.turns).toHaveLength(1) // existing preserved
    expect(c.lastError?.code).toBe('history_unavailable')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

```
cd web && npx vitest run src/features/sessions/useClaudeStateLoader.test.ts
```

Expected: file-not-found — hook doesn't exist.

- [ ] **Step 3: Write the hook**

```ts
import { useEffect } from 'react'
import { getClaudeState } from '../../lib/api'
import {
  ClaudeState,
  PerSessionState,
  emptyClaudeState,
  emptyPerSessionState,
} from './types'

interface Args {
  selectedSessionID: string | null
  perSession: Map<string, PerSessionState>
  setPerSession: (
    updater: (prev: Map<string, PerSessionState>) => Map<string, PerSessionState>
  ) => void
}

// useClaudeStateLoader fetches the server-authoritative ClaudeState
// once per session per page lifecycle. Idempotent — guarded by
// claude.turnsLoaded. Replaces useClaudeHistoryLoader. The previous
// loader had a "preserve local turns if any" merge branch; this one
// does not — the server is the truth source, so we overwrite in full
// on success.
//
// Failure modes:
//   - HTTP error: existing claude state (if any) is preserved; we
//     mark turnsLoaded=true so we don't loop, and surface a
//     lastError banner.
//   - Session not in claude UI mode: hook is a no-op.
export function useClaudeStateLoader({ selectedSessionID, perSession, setPerSession }: Args) {
  const ps = selectedSessionID ? perSession.get(selectedSessionID) : undefined
  const mode = ps?.mode
  const renderer = ps?.renderer
  const turnsLoaded = ps?.claude?.turnsLoaded === true

  useEffect(() => {
    if (!selectedSessionID) return
    if (mode !== 'claude' || renderer !== 'ui') return
    if (turnsLoaded) return

    let alive = true
    getClaudeState(selectedSessionID)
      .then((state: ClaudeState) => {
        if (!alive) return
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur = next.get(selectedSessionID) ?? emptyPerSessionState()
          next.set(selectedSessionID, {
            ...cur,
            claude: { ...state, turnsLoaded: true },
          })
          return next
        })
      })
      .catch((e: unknown) => {
        if (!alive) return
        const msg = e instanceof Error ? e.message : String(e)
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur = next.get(selectedSessionID) ?? emptyPerSessionState()
          const existing = cur.claude ?? emptyClaudeState()
          next.set(selectedSessionID, {
            ...cur,
            claude: {
              ...existing,
              turnsLoaded: true,
              lastError: { code: 'history_unavailable', message: msg },
            },
          })
          return next
        })
      })
    return () => { alive = false }
  }, [selectedSessionID, mode, renderer, turnsLoaded, setPerSession])
}
```

- [ ] **Step 4: Run test to verify it passes**

```
cd web && npx vitest run src/features/sessions/useClaudeStateLoader.test.ts
```

Expected: 3 PASS.

- [ ] **Step 5: Commit**

```
git add web/src/features/sessions/useClaudeStateLoader.ts web/src/features/sessions/useClaudeStateLoader.test.ts
git commit -m "feat(web): useClaudeStateLoader hook for server-truth hydrate

Single fetch per session per page lifecycle. On success, the server
state replaces local state in full. On HTTP error, existing local
state (if any) is preserved and lastError is set so the banner
appears. Replaces useClaudeHistoryLoader; the old loader is removed
in Task 4 after its caller is swapped.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 4: Swap the caller — `useSessions` uses the new hook

**Files:**
- Modify: `web/src/features/sessions/useSessions.ts`
- Delete: `web/src/features/sessions/useClaudeHistoryLoader.ts`
- Delete: `web/src/features/sessions/useClaudeHistoryLoader.test.ts`

- [ ] **Step 1: Find the import**

```
grep -n "useClaudeHistoryLoader" web/src/features/sessions/useSessions.ts
```

- [ ] **Step 2: Replace import and call**

```ts
// before
import { useClaudeHistoryLoader } from './useClaudeHistoryLoader'
// after
import { useClaudeStateLoader } from './useClaudeStateLoader'
```

```ts
// before
useClaudeHistoryLoader({ selectedSessionID, perSession, setPerSession })
// after
useClaudeStateLoader({ selectedSessionID, perSession, setPerSession })
```

- [ ] **Step 3: Delete the old hook files**

```
git rm web/src/features/sessions/useClaudeHistoryLoader.ts
git rm web/src/features/sessions/useClaudeHistoryLoader.test.ts
```

- [ ] **Step 4: Remove `getClaudeHistory` from api.ts**

In `web/src/lib/api.ts`, delete the `getClaudeHistory` function. The only caller is gone.

- [ ] **Step 5: Typecheck + tests**

```
cd web && npx tsc --noEmit && npx vitest run
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```
git add web/src/features/sessions/useSessions.ts web/src/lib/api.ts
git commit -m "refactor(web): switch hydrate to useClaudeStateLoader; drop history loader

Only caller of useClaudeHistoryLoader was useSessions — it now calls
useClaudeStateLoader. The old hook, its tests, and getClaudeHistory in
api.ts are removed in the same commit because keeping unused code
around invites drift. The server's /claude-history endpoint still
exists for one release cycle as a courtesy to any other client.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 5: Strip local timestamps from `claudeReducer`

**Files:**
- Modify: `web/src/features/sessions/claudeReducer.ts`

- [ ] **Step 1: Catalog every `new Date()` call**

```
grep -n "new Date()" web/src/features/sessions/claudeReducer.ts
```

You should see ~11 occurrences across `tool_use_start`, `tool_result`, `result`, `task_started`, `task_notification`, `task_updated`, `hook_started`, `hook_response`, `beginClaudeTurn`, `finalizeInFlightTurn`.

- [ ] **Step 2: Extend payload narrowing helpers to accept `started_at` / `finished_at`**

At the bottom of `claudeReducer.ts`, locate `asTextDelta`, `asToolUseStart`, etc. Add timestamp narrowing:

```ts
// Server stamps every WS frame's payload with the Apply-time wall
// clock; reducers read it into the corresponding state field instead
// of calling new Date(). When the server omits the field (older
// builds, transition period), fall back to the frame-level timestamp
// the WS handler attaches.
function asTimestamp(payload: unknown, frameTs?: string): string {
  const p = payload as { started_at?: string; finished_at?: string; timestamp?: string } | null
  return p?.finished_at ?? p?.started_at ?? p?.timestamp ?? frameTs ?? new Date().toISOString()
}
```

The `new Date().toISOString()` at the end is a final safety net for the brief transition period; remove it once Plan 3 ships and the dev server is rebuilt.

- [ ] **Step 3: Replace each `new Date().toISOString()` with the event's timestamp**

The reducer signature for `claude_event` cases is `(state, kind, payload)` — extend it to `(state, kind, payload, frameTs)` so the frame-level server timestamp is available. Update the producer (the WS handler in `useSessions.ts`) to pass `m.timestamp` when applying `claude_event`.

Apply, case by case:

```ts
case 'tool_use_start': {
  ...
  blocks.push({
    kind: 'tool',
    tool: {
      ...
      startedAt: asTimestamp(p, frameTs),
    },
  })
  ...
}

case 'tool_result': {
  ...
  last.blocks = patchToolBlock(last.blocks, p.toolUseId, (t) => ({
    ...t,
    result: p.content,
    isError: p.isError,
    finishedAt: asTimestamp(p, frameTs),
  }))
  ...
}

case 'result': {
  ...
  last.finishedAt = asTimestamp(p, frameTs)
  ...
}
```

For `task_started`, `task_notification`, `task_updated`, `hook_started`, `hook_response` — same pattern, read from payload or frame-level ts.

Inside `finalizeInFlightTurn` keep `new Date().toISOString()` for the backstop case (runner died, no event to read from); add a comment:

```ts
// finalizeInFlightTurn is called from non-event paths (run_ended,
// claude_error). When the server emits the corresponding frames
// it includes a timestamp; we forward it. Local fallback only
// when neither is available.
```

Update `finalizeInFlightTurn` to accept an optional `ts?: string`:

```ts
export function finalizeInFlightTurn(prev: ClaudeState, reason?: string, ts?: string): ClaudeState {
  ...
  const last = {
    ...turns[lastIdx],
    done: true,
    isError: true,
    finishedAt: ts ?? new Date().toISOString(),
  }
  ...
}
```

Update callers in `reduceClaudeMsg` to pass `m.timestamp` for `claude_run_ended` and `claude_error`.

`beginClaudeTurn` stays local — it runs **before** the WS round-trip, so its `startedAt` is a placeholder. The `turn_started` broadcast (Task 6) overwrites it with the server's value.

- [ ] **Step 4: Run reducer tests**

```
cd web && npx vitest run src/features/sessions/claudeReducer.test.ts
```

Expected: existing tests may break because they no longer get `new Date()` for free. Update each test that was feeding `tool_use_start` etc. without a `started_at` to include one:

```ts
s = applyClaudeEvent(s, 'tool_use_start', {
  index: 0,
  tool_use_id: 'tu_1',
  name: 'Bash',
  started_at: '2026-06-18T07:00:01Z',
})
```

And replace wall-clock assertions like `expect(parsed).toBeLessThan(t0 + 2_000)` with exact-value checks:

```ts
expect(block.tool.startedAt).toBe('2026-06-18T07:00:01Z')
```

Tests are now deterministic.

- [ ] **Step 5: Commit**

```
git add web/src/features/sessions/claudeReducer.ts web/src/features/sessions/claudeReducer.test.ts
git commit -m "refactor(web): reducer reads timestamps from event payloads

Every state field that carries a wall-clock time (tool startedAt /
finishedAt, turn finishedAt, bgTask startedAt, subagent startedAt)
now comes from the server-stamped event payload, with the frame-level
timestamp as a fallback during the transition. Tests no longer
exercise Date.now() — they feed explicit timestamps and assert
exact values.

beginClaudeTurn keeps a local placeholder startedAt; the next
commit (turn_started reconciliation) replaces it with the
server-authoritative value.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 6: Optimistic-turn reconciliation via `turn_started`

**Files:**
- Modify: `web/src/lib/ws.ts` (new frame types)
- Modify: `web/src/features/sessions/claudeReducer.ts` (handle `turn_started`)
- Modify: `web/src/features/sessions/useSessions.ts` (generate `clientNonce`)
- Modify: `web/src/features/sessions/claudeReducer.test.ts`

- [ ] **Step 1: Write the failing reducer test**

Append to `claudeReducer.test.ts`:

```ts
describe('reduceClaudeMsg: turn_started reconciliation', () => {
  it('replaces a placeholder turn matched by clientNonce', () => {
    const sid = 'sess1'
    let perSession = new Map()
    // Optimistic: beginClaudeTurn with a client nonce.
    let s = beginClaudeTurn(emptyClaudeState(), 'hi', { clientNonce: 'nonce-1' })
    perSession.set(sid, {
      running: null, messages: [], messagesLoaded: false,
      mode: 'claude', renderer: 'ui', claude: s,
    })
    const next = reduceClaudeMsg(perSession, {
      type: 'turn_started',
      sessionID: sid,
      clientNonce: 'nonce-1',
      turnId: 'server-turn-id-xyz',
      timestamp: '2026-06-18T07:00:00Z',
    } as any)!
    const turn = next.get(sid)!.claude!.turns[0]
    expect(turn.id).toBe('server-turn-id-xyz')
    expect(turn.startedAt).toBe('2026-06-18T07:00:00Z')
    expect(turn.prompt).toBe('hi') // user-typed prompt preserved
  })
})

describe('reduceClaudeMsg: tool_decision_applied', () => {
  it('sets the matching tool block decision to the server value', () => {
    const sid = 'sess1'
    let perSession = new Map()
    let s = beginClaudeTurn(emptyClaudeState(), 'use a tool')
    s = applyClaudeEvent(s, 'tool_use_start', {
      index: 0, tool_use_id: 'tu_1', name: 'Bash',
      started_at: '2026-06-18T07:00:00Z',
    })
    perSession.set(sid, {
      running: null, messages: [], messagesLoaded: false,
      mode: 'claude', renderer: 'ui', claude: s,
    })
    const next = reduceClaudeMsg(perSession, {
      type: 'tool_decision_applied',
      sessionID: sid,
      toolUseId: 'tu_1',
      decision: 'deny',
    } as any)!
    const tool = next.get(sid)!.claude!.turns[0].blocks[0]
    expect(tool.kind).toBe('tool')
    if (tool.kind === 'tool') expect(tool.tool.decision).toBe('deny')
  })
})
```

- [ ] **Step 2: Add frame types**

In `web/src/lib/ws.ts`, extend `ServerMsg`:

```ts
| { type: 'turn_started'; sessionID: string; clientNonce: string; turnId: string; timestamp: string }
| { type: 'tool_decision_applied'; sessionID: string; toolUseId: string; decision: 'allow' | 'deny' }
```

- [ ] **Step 3: Update `beginClaudeTurn` to accept and store the nonce**

```ts
interface BeginOpts {
  clientNonce?: string
}

export function beginClaudeTurn(prev: ClaudeState, prompt: string, opts: BeginOpts = {}): ClaudeState {
  const turn: ClaudeTurn = {
    id: opts.clientNonce ? `pending:${opts.clientNonce}` : randomId(),
    prompt,
    startedAt: new Date().toISOString(), // placeholder; reconciled on turn_started
    blocks: [],
    done: false,
  }
  return { ...prev, turns: [...prev.turns, turn], inFlight: true, lastError: undefined }
}
```

- [ ] **Step 4: Handle `turn_started` and `tool_decision_applied` in `reduceClaudeMsg`**

```ts
case 'turn_started': {
  const c = prev.get(m.sessionID)?.claude ?? emptyClaudeState()
  const turns = c.turns.map((t) =>
    t.id === `pending:${m.clientNonce}`
      ? { ...t, id: m.turnId, startedAt: m.timestamp }
      : t
  )
  return mutateClaude(prev, m.sessionID, (cc) => ({ ...cc, turns }))
}
case 'tool_decision_applied': {
  return mutateClaude(prev, m.sessionID, (c) => ({
    ...c,
    turns: c.turns.map((t) => ({
      ...t,
      blocks: patchToolBlock(t.blocks, m.toolUseId, (tool) => ({ ...tool, decision: m.decision })),
    })),
  }))
}
```

- [ ] **Step 5: Generate nonce in `useSessions.onPrompt`**

Find the spot where `useSessions` sends `claude_prompt`:

```
grep -n "claude_prompt" web/src/features/sessions/useSessions.ts
```

Wrap the send:

```ts
const nonce = randomId(16)
setPerSession((prev) =>
  mutateClaude(prev, sessionID, (c) => beginClaudeTurn(c, text, { clientNonce: nonce }))
)
ws.send({ type: 'claude_prompt', sessionID, text, opts: { ..., clientNonce: nonce } })
```

Import `randomId` from `../../lib/randomId`.

- [ ] **Step 6: Run tests**

```
cd web && npx vitest run
```

Expected: all PASS, including the two new ones.

- [ ] **Step 7: Commit**

```
git add web/src/features/sessions/claudeReducer.ts web/src/features/sessions/claudeReducer.test.ts web/src/features/sessions/useSessions.ts web/src/lib/ws.ts
git commit -m "feat(web): reconcile optimistic turn via turn_started + apply decisions

clientNonce travels with claude_prompt; beginClaudeTurn parks the
placeholder turn under id 'pending:<nonce>'; the server-broadcast
turn_started frame swaps the id and the authoritative startedAt.
tool_decision_applied frames overwrite the optimistically-set
decision so other connected tabs converge on the server value.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 7: Manual smoke test against the dev server

- [ ] **Step 1: Start the backend with the new code**

```
make run
```

- [ ] **Step 2: Open the UI in a browser, enter Claude mode, send a prompt**

Watch the network panel: you should see exactly one `GET /api/sessions/<id>/claude-state` and a stream of WS frames including `turn_started` once, `claude_event` repeatedly, then `result` and `tool_decision_applied` if you approve / deny a tool.

- [ ] **Step 3: Refresh the page**

The same turn should re-appear with cost, per-tool elapsed, decision label all intact. This is the visual evidence the refactor achieves its goal.

- [ ] **Step 4: Hit it with two tabs**

Two browser tabs on the same session, send a prompt in tab A, watch the turn appear in tab B as `turn_started` is broadcast. Decide a tool in tab A, watch the badge update in tab B via `tool_decision_applied`.

- [ ] **Step 5: Commit branch state**

(No code change in this task — just verification.)

---

## Task 8: Playwright refresh-parity test

**Files:**
- Create: `web/e2e/refresh-parity.spec.ts`

- [ ] **Step 1: Write the test**

```ts
import { test, expect } from '@playwright/test'

// Visible-string parity: the cost / elapsed / decision rendered before
// reload must be identical to what re-renders after reload. This is
// the user-facing contract the entire refactor delivers.
test('Claude UI: refresh preserves cost, elapsed, decision', async ({ page }) => {
  await page.goto('/')
  await page.getByRole('textbox', { name: /login/i }).fill(process.env.ALFRED_DEV_TOKEN || '')
  await page.getByRole('button', { name: /sign in/i }).click()

  // Enter a session and open Claude UI mode.
  await page.getByRole('button', { name: /new session/i }).click()
  await page.getByRole('button', { name: /enter claude/i }).click()

  // Send a prompt that triggers a Bash tool call so we can assert
  // tool elapsed survives.
  await page.getByPlaceholder('Message Claude…').fill('list files in current directory with ls')
  await page.getByRole('button', { name: /send/i }).click()

  // Wait for the turn to finish (Done chip or footer cost appearing).
  const footer = page.locator('.claude-turn__footer').first()
  await expect(footer).toBeVisible({ timeout: 30_000 })

  // Snapshot the visible strings.
  const beforeCost = await footer.locator('text=/\\$[0-9.]+/').textContent()
  const beforeElapsed = await footer.locator('text=/^\\d+s$/').first().textContent()
  const beforeToolElapsed = await page.locator('.claude-tool__elapsed').first().textContent()
  const beforeStatus = await page.locator('.claude-tool__status').first().textContent()

  // Reload and re-find the same elements.
  await page.reload()
  await page.waitForSelector('.claude-turn__footer')

  expect(await footer.locator('text=/\\$[0-9.]+/').textContent()).toBe(beforeCost)
  expect(await footer.locator('text=/^\\d+s$/').first().textContent()).toBe(beforeElapsed)
  expect(await page.locator('.claude-tool__elapsed').first().textContent()).toBe(beforeToolElapsed)
  expect(await page.locator('.claude-tool__status').first().textContent()).toBe(beforeStatus)
})
```

- [ ] **Step 2: Run the test**

```
cd web && npx playwright test refresh-parity --reporter=list
```

Expected: PASS. If it flakes on the 30s timeout, raise to 60s — claude turns vary in length.

- [ ] **Step 3: Commit**

```
git add web/e2e/refresh-parity.spec.ts
git commit -m "test(e2e): visible-string refresh parity for cost/elapsed/decision

Sends a Bash-using prompt, captures the footer cost, turn elapsed,
tool elapsed, and tool decision label as visible strings, reloads
the page, and asserts the same strings re-appear. This is the user-
facing contract the entire refactor delivers.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 9: Final full-suite run + PR prep

- [ ] **Step 1: Full backend + race**

```
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred
go test ./... -race 2>&1 | tail -15
```

Expected: PASS (cmd/alfred-server may fail on port conflict; ignore).

- [ ] **Step 2: Full frontend**

```
cd web && npx tsc --noEmit && npx vitest run && npx playwright test
```

Expected: all PASS.

- [ ] **Step 3: Push and open PR**

```
git push origin refactor/refresh-parity
gh pr create --title "refactor: Claude UI refresh parity (server as truth source)" --body "$(cat <<'EOF'
## Summary

- Server is the single source of truth for Claude UI state per session
- New internal/claudestate package: types, Apply reducer, debounced atomic snapshot Persister, Loader with jsonl+snapshot merge
- New GET /api/sessions/{sid}/claude-state endpoint returning the full ClaudeState
- WS event ingestion routed through SessionState.Apply before broadcast — guarantees server state ≡ what any client can reconstruct
- New broadcast frames: turn_started (optimistic-turn reconciliation), tool_decision_applied (multi-tab convergence)
- Frontend reducer no longer stamps timestamps locally; all server-authoritative fields hydrate from /claude-state and update from WS event payloads

## Test plan
- [x] go test ./... -race
- [x] vitest run
- [x] playwright refresh-parity (sends a Bash tool turn, refreshes, asserts cost/elapsed/decision strings unchanged)
- [x] Manual: two browser tabs converge on tool_decision_applied
EOF
)"
```

---

## Spec coverage check (self-review)

| Spec requirement | Plan 3 task |
| --- | --- |
| Frontend hydrates from `/claude-state` | Tasks 2, 3, 4 |
| Frontend reducer reads server timestamps | Task 5 |
| Optimistic-UI reconciliation via `clientNonce` | Task 6 |
| `tool_decision_applied` applied client-side | Task 6 |
| Playwright refresh-parity assertion | Task 8 |
| Old `/claude-history` callers removed | Task 4 |
| TS types mirror Go types | Task 1 |

Out of scope (intentional):
- Removing the `/claude-history` HTTP endpoint server-side (one-release-cycle deprecation per spec)
- OpenAPI / TS codegen (YAGNI; manual mirror suffices for now)
- WS resync frame for `bgTasks` / `subagents` (these stay server-broadcast and reconnect-replay)
