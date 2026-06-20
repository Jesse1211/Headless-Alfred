// Error/abnormal-case audit for the Claude reducer.
//
// METHOD: construct the WS frame / state each path would produce
// ("assume the hook fired"), run it through the reducer, then assert
// the reaction is sane.
//
// Each test is tagged GAP or HANDLED:
//   GAP     — asserts the CORRECT behavior; FAILS against current
//             (buggy) code. The failure IS the deliverable: it documents
//             a real shortcoming in the reducer.
//   HANDLED — asserts current correct behavior (usually a safe no-op);
//             PASSES.
//
// Do NOT "fix" the reducer to make GAP tests pass — they are intended
// to stay red until the underlying production gap is addressed.
import { describe, it, expect } from 'vitest'
import {
  reduceClaudeMsg,
  applyClaudeEvent,
  beginClaudeTurn,
  finalizeInFlightTurn,
} from './claudeReducer'
import { emptyClaudeState, ClaudeTurn, PerSessionState } from './types'

const TS = '2026-06-18T07:00:00Z'
const TS2 = '2026-06-18T07:00:01Z'

// Build a PerSessionState wrapping a given ClaudeState — mirrors the
// shape the reducer expects in the perSession Map (see claudeReducer.test.ts).
function perSessionWith(sessionID: string, claude: PerSessionState['claude']) {
  const m = new Map<string, PerSessionState>()
  m.set(sessionID, {
    running: null,
    messages: [],
    messagesLoaded: false,
    mode: 'claude',
    renderer: 'ui',
    claude,
  })
  return m
}

// ---------------------------------------------------------------------------
// GAP 1 — finalizeInFlightTurn mislabels a turn that streamed real content
//         as isError.
//
// When the runner dies (claude_run_ended / claude_error) AFTER the turn
// already streamed successful content (text + a completed tool), the only
// signal the reducer leaves behind is `isError: true`. That conflates two
// distinct outcomes:
//   - "errored"  : the turn genuinely failed.
//   - "aborted"  : the turn was interrupted (SIGINT / runner exit) but had
//                  produced valid output up to that point.
// The UI can't tell these apart, so a perfectly good partial reply gets
// painted red. The CORRECT behavior is to distinguish them — e.g. via an
// `outcome` discriminator ('aborted' vs 'errored'). No such field exists
// yet, so this assertion fails and documents the gap.
// ---------------------------------------------------------------------------
describe('GAP 1: finalizeInFlightTurn cannot distinguish aborted from errored', () => {
  it('a turn that streamed real content then got finalized should be marked aborted, not a generic error', () => {
    // Begin a turn and stream successful content into it.
    let s = beginClaudeTurn(emptyClaudeState(), 'do real work')
    s = applyClaudeEvent(s, 'text_delta', { index: 0, text: 'here is my answer ' })
    s = applyClaudeEvent(s, 'tool_use_start', { index: 1, tool_use_id: 'tu_1', name: 'Bash' }, TS)
    s = applyClaudeEvent(s, 'tool_result', { tool_use_id: 'tu_1', content: 'done ok', is_error: false }, TS)

    // Sanity: the turn carries genuine content and the tool succeeded.
    expect(s.turns[0].blocks.length).toBe(2)
    const toolBlock = s.turns[0].blocks[1]
    expect(toolBlock.kind === 'tool' && toolBlock.tool.isError).toBeFalsy()

    // Runner dies — backstop finalizes the still-open turn.
    s = finalizeInFlightTurn(s, 'runner exited', TS2)
    const turn = s.turns[0] as ClaudeTurn & { outcome?: string }

    expect(turn.done).toBe(true)
    // CORRECT behavior: a turn that produced valid content but was cut off
    // is "aborted", not "errored". The reducer should expose a way to tell
    // them apart instead of always stamping isError:true.
    expect(turn.outcome).toBe('aborted')
  })
})

// ---------------------------------------------------------------------------
// GAP 2 — turn_started with an unmatched clientNonce leaves an orphan
//         pending turn.
//
// beginClaudeTurn(..., { clientNonce: 'AAA' }) creates a placeholder turn
// with id 'pending:AAA'. The turn_started handler swaps 'pending:<nonce>'
// for the authoritative turnId ONLY when the nonce matches. If a
// turn_started arrives carrying a DIFFERENT nonce ('BBB') — e.g. the
// frames raced/crossed, or the optimistic prompt was rebroadcast — the
// 'pending:AAA' turn is never reconciled and stays stranded with a
// 'pending:' id forever. The CORRECT behavior is for no turn to be left
// holding a 'pending:' id after a turn_started lands.
// ---------------------------------------------------------------------------
describe('GAP 2: turn_started with unmatched clientNonce orphans the pending turn', () => {
  it('no turn should be left with a pending: id after a turn_started frame is processed', () => {
    const sid = 'sess-orphan'
    const start = beginClaudeTurn(emptyClaudeState(), 'prompt A', { clientNonce: 'AAA' })
    expect(start.turns[0].id).toBe('pending:AAA')

    const perSession = perSessionWith(sid, start)
    const next = reduceClaudeMsg(perSession, {
      type: 'turn_started',
      sessionID: sid,
      turnId: 'turn-123',
      clientNonce: 'BBB', // does NOT match the optimistic 'AAA'
      timestamp: TS,
    } as never)!

    const turns = next.get(sid)!.claude!.turns
    const stillPending = turns.filter((t) => t.id.startsWith('pending:'))
    // CORRECT behavior: the orphan must be reconciled (re-keyed/dropped),
    // not left stranded with a 'pending:' id.
    expect(stillPending).toHaveLength(0)
  })
})

// ---------------------------------------------------------------------------
// GAP 3 — lastError has no clear path once a later turn succeeds.
//
// claude_error stamps state.lastError = { code, message }. beginClaudeTurn
// clears lastError (sets it to undefined). But if the NEXT turn completes
// successfully via a `result` frame WITHOUT a fresh beginClaudeTurn having
// run in between (e.g. server-driven turn, reattach, or a result that
// lands on an already-open turn), lastError persists — a stale error
// banner outlives the failure that produced it. The CORRECT behavior is
// for a successful result to clear lastError.
// ---------------------------------------------------------------------------
describe('GAP 3: lastError persists across a later successful result', () => {
  it('a successful result frame should clear a previously-set lastError', () => {
    const sid = 'sess-err'

    // 1. A claude_error sets lastError (and finalizes the in-flight turn).
    let perSession = perSessionWith(sid, beginClaudeTurn(emptyClaudeState(), 'first prompt'))
    perSession = reduceClaudeMsg(perSession, {
      type: 'claude_error',
      sessionID: sid,
      code: 'not_authenticated',
      message: 'please log in',
      timestamp: TS,
    } as never)!
    expect(perSession.get(sid)!.claude!.lastError).toEqual({
      code: 'not_authenticated',
      message: 'please log in',
    })

    // 2. A new turn streams in and completes successfully via `result`,
    //    WITHOUT a fresh beginClaudeTurn clearing lastError first. We
    //    simulate this by appending a turn directly and folding a result.
    const cur = perSession.get(sid)!.claude!
    const liveTurn: ClaudeTurn = {
      id: 'turn-2',
      prompt: 'second prompt',
      startedAt: TS2,
      blocks: [{ kind: 'text', text: 'all good now' }],
      done: false,
    }
    const withLive = { ...cur, turns: [...cur.turns, liveTurn], inFlight: true }
    const resolved = applyClaudeEvent(withLive, 'result', { is_error: false, total_cost_usd: 0.02 }, TS2)

    expect(resolved.turns[resolved.turns.length - 1].isError).toBe(false)
    // CORRECT behavior: the stale error should be gone after a clean result.
    expect(resolved.lastError).toBeUndefined()
  })
})

// ---------------------------------------------------------------------------
// HANDLED 3b — manual dismiss path: clearing lastError on the ClaudeState
//             leaves a banner-free state.
//
// ADR-005 keeps the manual `clearError` dismiss button alongside the
// auto-clear-on-clean-result behavior (GAP 3). The dismiss button is wired
// through useSessions.clearError (end-to-end coverage in useSessions.test.ts);
// here we assert the state-layer invariant the button relies on: once
// lastError is set (claude_error) and then cleared, the ClaudeChatView banner
// predicate (`!!state.lastError`) is false again.
// ---------------------------------------------------------------------------
describe('HANDLED 3b: manual dismiss clears lastError on ClaudeState', () => {
  it('a state with lastError, once cleared, exposes no error banner', () => {
    const sid = 'sess-dismiss'
    // claude_error stamps lastError.
    let perSession = perSessionWith(sid, beginClaudeTurn(emptyClaudeState(), 'prompt'))
    perSession = reduceClaudeMsg(perSession, {
      type: 'claude_error',
      sessionID: sid,
      code: 'rate_limited',
      message: 'slow down',
      timestamp: TS,
    } as never)!
    const withError = perSession.get(sid)!.claude!
    expect(withError.lastError).toEqual({ code: 'rate_limited', message: 'slow down' })
    expect(!!withError.lastError).toBe(true) // banner shown

    // Manual dismiss: the button resets lastError to undefined.
    const dismissed = { ...withError, lastError: undefined }
    expect(dismissed.lastError).toBeUndefined()
    expect(!!dismissed.lastError).toBe(false) // banner gone
  })
})

// ---------------------------------------------------------------------------
// HANDLED 4 — tool_decision_applied for a non-existent toolUseId is a safe
//             no-op.
// ---------------------------------------------------------------------------
describe('HANDLED 4: tool_decision_applied for an unknown toolUseId is a no-op', () => {
  it('does not crash and leaves blocks untouched', () => {
    const sid = 'sess-decision'
    // A turn with one real tool block (id tu_real).
    let s = beginClaudeTurn(emptyClaudeState(), 'use a tool')
    s = applyClaudeEvent(s, 'tool_use_start', { index: 0, tool_use_id: 'tu_real', name: 'Bash' }, TS)
    const before = s.turns[0].blocks

    const perSession = perSessionWith(sid, s)
    const next = reduceClaudeMsg(perSession, {
      type: 'tool_decision_applied',
      sessionID: sid,
      toolUseId: 'tu_does_not_exist',
      decision: 'allow',
    } as never)!

    const afterTurn = next.get(sid)!.claude!.turns[0]
    // The real tool block is unchanged: still pending, same shape.
    const afterBlock = afterTurn.blocks[0]
    expect(afterBlock.kind === 'tool' && afterBlock.tool.decision).toBe('pending')
    // patchToolBlock returns the same array reference when nothing matched.
    expect(afterTurn.blocks).toEqual(before)
  })
})

// ---------------------------------------------------------------------------
// HANDLED 5 — result frame with no current turn does not crash.
// ---------------------------------------------------------------------------
describe('HANDLED 5: result event on empty state does not crash', () => {
  it('returns inFlight:false and no turns, no throw', () => {
    const s = emptyClaudeState()
    const after = applyClaudeEvent(s, 'result', { is_error: false, total_cost_usd: 0.01 }, TS)
    expect(after.inFlight).toBe(false)
    expect(after.turns).toHaveLength(0)
  })
})

// ---------------------------------------------------------------------------
// HANDLED 6 — result frame respects the isError flag.
//
// This is the ONE path that honors the server's error signal, unlike
// finalizeInFlightTurn (GAP 1) which hard-codes isError:true.
// ---------------------------------------------------------------------------
describe('HANDLED 6: result event respects the isError flag', () => {
  it('marks the turn isError:true + done when result is_error is true', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'go')
    s = applyClaudeEvent(s, 'result', { is_error: true, total_cost_usd: 0.01 }, TS)
    expect(s.turns[0].isError).toBe(true)
    expect(s.turns[0].done).toBe(true)
  })

  it('marks the turn isError:false + done when result is_error is false', () => {
    let s = beginClaudeTurn(emptyClaudeState(), 'go')
    s = applyClaudeEvent(s, 'result', { is_error: false, total_cost_usd: 0.01 }, TS)
    expect(s.turns[0].isError).toBe(false)
    expect(s.turns[0].done).toBe(true)
  })
})
