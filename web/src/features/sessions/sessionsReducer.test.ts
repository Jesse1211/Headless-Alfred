import { describe, it, expect } from 'vitest'
import {
  reducePerSession, applyAuthoritativeRecord,
  applyClaudeEvent, beginClaudeTurn, resolveClaudeTool, finalizeInFlightTurn,
} from './sessionsReducer'
import { PerSessionState, ClaudeTurn, emptyPerSessionState, emptyClaudeState, joinAssistantText } from './types'

const id = (s: string) => Buffer.from(s, 'utf8').toString('base64')
const b64decode = (s: string) => Buffer.from(s, 'base64').toString('utf8')

// Tests previously asserted turn.text (a single concatenated string).
// The new turn shape stores text + tools interleaved in turn.blocks,
// so tests use joinAssistantText() to recover the equivalent.
const turnText = (t: ClaudeTurn) => joinAssistantText(t.blocks)

describe('reducePerSession', () => {
  it('idle initializes per-session state', () => {
    const { perSession } = reducePerSession(new Map(), { type: 'idle', sessionID: 'A' }, b64decode)
    expect(perSession.get('A')?.running).toBeNull()
  })

  it('started populates running', () => {
    const { perSession } = reducePerSession(
      new Map(),
      { type: 'started', sessionID: 'A', cmdId: 'c1', command: 'ls', startedAt: 't' },
      b64decode,
    )
    expect(perSession.get('A')?.running?.id).toBe('c1')
  })

  it('chunk appends decoded data', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), running: { id: 'c1', command: 'ls', startedAt: 't', output: 'a', truncatedLossWarned: false } }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'chunk', sessionID: 'A', cmdId: 'c1', data: id('bc') },
      b64decode,
    )
    expect(perSession.get('A')?.running?.output).toBe('abc')
  })

  it('chunk for wrong cmdId is ignored', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), running: { id: 'c1', command: 'ls', startedAt: 't', output: 'a', truncatedLossWarned: false } }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'chunk', sessionID: 'A', cmdId: 'OTHER', data: id('bc') },
      b64decode,
    )
    expect(perSession).toBe(seed) // unchanged ref
  })

  it('done moves running into messages with status completed (any exit code)', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), running: { id: 'c1', command: 'ls', startedAt: 't', output: 'out', truncatedLossWarned: false } }],
    ])
    const { perSession, fetchCommandForSession } = reducePerSession(
      seed,
      { type: 'done', sessionID: 'A', cmdId: 'c1', exitCode: 137, finishedAt: 't2' },
      b64decode,
    )
    const msgs = perSession.get('A')!.messages
    expect(msgs).toHaveLength(1)
    expect(msgs[0].status).toBe('completed')
    expect(msgs[0].exitCode).toBe(137)
    expect(perSession.get('A')?.running).toBeNull()
    expect(fetchCommandForSession).toEqual({ sessionID: 'A', cmdID: 'c1' })
  })

  it('done is idempotent when running is already null', () => {
    // After the original done has moved the cmd into messages and
    // cleared running, a duplicate done frame should be a no-op.
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), messages: [{ id: 'c1', command: 'ls', output: '', startedAt: 't', finishedAt: 't2', exitCode: 0, status: 'completed', truncated: false }], running: null }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'done', sessionID: 'A', cmdId: 'c1', exitCode: 0, finishedAt: 't2' },
      b64decode,
    )
    expect(perSession).toBe(seed)
  })

  it('done clears stale phantom running with same cmdId (cleanup path)', () => {
    // Scenario: a duplicate `started` for an already-finished cmdId
    // resurrected a phantom running turn. The matching duplicate
    // `done` MUST clear that running, otherwise the UI stays
    // stuck on "live" forever. (This is the regression that
    // caused the bug where a quick `echo` showed as live.)
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(),
        messages: [{ id: 'c1', command: 'ls', output: 'real', startedAt: 't', finishedAt: 't2', exitCode: 0, status: 'completed', truncated: false }],
        running: { id: 'c1', command: 'ls', startedAt: 't', output: '', truncatedLossWarned: false },
      }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'done', sessionID: 'A', cmdId: 'c1', exitCode: 0, finishedAt: 't2' },
      b64decode,
    )
    expect(perSession.get('A')!.running).toBeNull()
    // messages preserved, NOT duplicated.
    expect(perSession.get('A')!.messages).toHaveLength(1)
  })

  it('started for already-completed cmdId is ignored (no phantom resurrection)', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(),
        messages: [{ id: 'c1', command: 'ls', output: 'real', startedAt: 't', finishedAt: 't2', exitCode: 0, status: 'completed', truncated: false }],
        running: null,
      }],
    ])
    const r = reducePerSession(
      seed,
      { type: 'started', sessionID: 'A', cmdId: 'c1', command: 'ls', startedAt: 't' },
      b64decode,
    )
    expect(r.perSession).toBe(seed)
  })

  it('full duplicate sequence (started, chunk, done) twice ends with one message and no running', () => {
    let s = new Map<string, PerSessionState>()
    const frames = [
      { type: 'started', sessionID: 'A', cmdId: 'X', command: 'echo hi', startedAt: 't1' },
      { type: 'chunk',   sessionID: 'A', cmdId: 'X', data: id('hi\n') },
      { type: 'done',    sessionID: 'A', cmdId: 'X', exitCode: 0, finishedAt: 't2' },
      { type: 'started', sessionID: 'A', cmdId: 'X', command: 'echo hi', startedAt: 't1' },
      { type: 'chunk',   sessionID: 'A', cmdId: 'X', data: id('hi\n') },
      { type: 'done',    sessionID: 'A', cmdId: 'X', exitCode: 0, finishedAt: 't2' },
    ] as const
    for (const f of frames) {
      s = reducePerSession(s, f as any, b64decode).perSession
    }
    const ps = s.get('A')!
    expect(ps.running).toBeNull()
    expect(ps.messages).toHaveLength(1)
    expect(ps.messages[0].output).toBe('hi\n')
  })

  it('duplicate chunk (same tail bytes) is not appended twice', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), running: { id: 'c1', command: 'ls', startedAt: 't', output: 'abc', truncatedLossWarned: false } }],
    ])
    // First chunk extends output.
    const s1 = reducePerSession(seed, { type: 'chunk', sessionID: 'A', cmdId: 'c1', data: id('de') } as any, b64decode).perSession
    expect(s1.get('A')!.running!.output).toBe('abcde')
    // Same chunk again — must NOT double-append.
    const s2 = reducePerSession(s1, { type: 'chunk', sessionID: 'A', cmdId: 'c1', data: id('de') } as any, b64decode).perSession
    expect(s2.get('A')!.running!.output).toBe('abcde')
  })
})

describe('applyAuthoritativeRecord', () => {
  it('replaces the matching completed message', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), messages: [{ id: 'c1', command: 'old', output: 'stale', startedAt: 't', finishedAt: 't2', exitCode: 0, status: 'completed', truncated: false }] }],
    ])
    const next = applyAuthoritativeRecord(seed, 'A', {
      id: 'c1', command: 'fresh', output: 'real', started_at: 't', finished_at: 't2', exit_code: 0, status: 'completed', output_truncated: false,
    })
    expect(next.get('A')!.messages[0].command).toBe('fresh')
    expect(next.get('A')!.messages[0].output).toBe('real')
  })

  it('no-op when sessionID unknown', () => {
    const seed = new Map<string, PerSessionState>()
    const next = applyAuthoritativeRecord(seed, 'A', { id: 'c1', command: '', output: '', started_at: '', status: 'completed', output_truncated: false })
    expect(next).toBe(seed)
  })
})

describe('claude UI reducer', () => {
  it('claude_entered with renderer=ui initialises claude state', () => {
    const { perSession } = reducePerSession(
      new Map(),
      { type: 'claude_entered', sessionID: 'A', renderer: 'ui' },
      b64decode,
    )
    const ps = perSession.get('A')!
    expect(ps.mode).toBe('claude')
    expect(ps.renderer).toBe('ui')
    expect(ps.claude).toEqual(emptyClaudeState())
  })

  it('claude_entered with renderer=tui leaves claude state undefined', () => {
    const { perSession } = reducePerSession(
      new Map(),
      { type: 'claude_entered', sessionID: 'A', renderer: 'tui' },
      b64decode,
    )
    const ps = perSession.get('A')!
    expect(ps.renderer).toBe('tui')
    expect(ps.claude).toBeUndefined()
  })

  it('claude_event with text_delta appends to the current turn text', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: beginClaudeTurn(emptyClaudeState(), 'hi') }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'claude_event', sessionID: 'A', eventKind: 'text_delta', payload: { text: 'Hello' } },
      b64decode,
    )
    const turns = perSession.get('A')!.claude!.turns
    expect(turnText(turns[0])).toBe('Hello')
  })

  it('claude_event result marks the turn done and clears inFlight', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: beginClaudeTurn(emptyClaudeState(), 'hi') }],
    ])
    const { perSession } = reducePerSession(
      seed,
      {
        type: 'claude_event', sessionID: 'A', eventKind: 'result',
        payload: { is_error: false, total_cost_usd: 0.0012, result: 'done' },
      },
      b64decode,
    )
    const c = perSession.get('A')!.claude!
    expect(c.inFlight).toBe(false)
    expect(c.turns[0].done).toBe(true)
    expect(c.turns[0].totalCostUsd).toBeCloseTo(0.0012)
  })

  it('tool_approval_request enqueues a pending request', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: emptyClaudeState() }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'tool_approval_request', sessionID: 'A', toolUseId: 't1', tool: 'Bash', toolInput: { cmd: 'ls' } },
      b64decode,
    )
    expect(perSession.get('A')!.claude!.pending).toEqual([
      { toolUseId: 't1', tool: 'Bash', input: { cmd: 'ls' } },
    ])
  })

  it('tool_approval_request is idempotent on duplicate IDs', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', {
        ...emptyPerSessionState(),
        renderer: 'ui',
        claude: {
          ...emptyClaudeState(),
          pending: [{ toolUseId: 't1', tool: 'Bash', input: { cmd: 'ls' } }],
        },
      }],
    ])
    const r = reducePerSession(
      seed,
      { type: 'tool_approval_request', sessionID: 'A', toolUseId: 't1', tool: 'Bash', toolInput: { cmd: 'ls' } },
      b64decode,
    )
    expect(r.perSession).toBe(seed)
  })

  it('claude_error records lastError and clears inFlight', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: beginClaudeTurn(emptyClaudeState(), 'hi') }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'claude_error', sessionID: 'A', code: 'CLAUDE_UNAVAILABLE', message: 'boom' },
      b64decode,
    )
    const c = perSession.get('A')!.claude!
    expect(c.inFlight).toBe(false)
    expect(c.lastError).toEqual({ code: 'CLAUDE_UNAVAILABLE', message: 'boom' })
  })

  it('resolveClaudeTool removes pending and tags decision on matching tool', () => {
    const c = {
      ...emptyClaudeState(),
      pending: [{ toolUseId: 't1', tool: 'Bash', input: {} }],
      turns: [{
        id: 'turn-1', prompt: 'p', startedAt: 't',
        blocks: [{
          kind: 'tool' as const,
          tool: { toolUseId: 't1', name: 'Bash', decision: 'pending' as const },
        }],
        done: false,
      }],
    }
    const next = resolveClaudeTool(c, 't1', 'allow')
    expect(next.pending).toEqual([])
    const block0 = next.turns[0].blocks[0]
    expect(block0.kind).toBe('tool')
    if (block0.kind === 'tool') expect(block0.tool.decision).toBe('allow')
  })

  it('applyClaudeEvent handles tool_use_start, tool_use_end, tool_result in order', () => {
    let c = beginClaudeTurn(emptyClaudeState(), 'do x')
    c = applyClaudeEvent(c, 'tool_use_start', { index: 0, tool_use_id: 't1', name: 'Bash' })
    const firstBlock = c.turns[0].blocks[0]
    expect(firstBlock.kind).toBe('tool')
    if (firstBlock.kind === 'tool') {
      expect(firstBlock.tool).toMatchObject({ toolUseId: 't1', name: 'Bash', decision: 'pending' })
    }
    c = applyClaudeEvent(c, 'tool_use_end', { tool_use_id: 't1', input: { cmd: 'ls' } })
    const afterEnd = c.turns[0].blocks[0]
    if (afterEnd.kind === 'tool') expect(afterEnd.tool.input).toEqual({ cmd: 'ls' })
    c = applyClaudeEvent(c, 'tool_result', { tool_use_id: 't1', content: 'a\nb', is_error: false })
    const afterResult = c.turns[0].blocks[0]
    if (afterResult.kind === 'tool') {
      expect(afterResult.tool.result).toBe('a\nb')
      expect(afterResult.tool.isError).toBe(false)
    }
  })

  it('claude_exited preserves turn history but clears in-flight + pending', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', {
        ...emptyPerSessionState(),
        mode: 'claude',
        renderer: 'ui',
        claude: {
          ...emptyClaudeState(),
          inFlight: true,
          pending: [{ toolUseId: 't1', tool: 'Bash', input: {} }],
          turns: [{
            id: 'turn-1', prompt: 'p', startedAt: 't',
            blocks: [{ kind: 'text' as const, text: 'reply' }],
            done: true,
          }],
        },
      }],
    ])
    const { perSession } = reducePerSession(seed, { type: 'claude_exited', sessionID: 'A' }, b64decode)
    const ps = perSession.get('A')!
    expect(ps.mode).toBe('shell')
    expect(ps.renderer).toBe('')
    expect(ps.claude!.inFlight).toBe(false)
    expect(ps.claude!.pending).toEqual([])
    expect(ps.claude!.turns.length).toBe(1)
  })

  it('claude_exited clears turnsLoaded', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', {
        ...emptyPerSessionState(),
        mode: 'claude',
        renderer: 'ui',
        claude: { ...emptyClaudeState(), turnsLoaded: true },
      }],
    ])
    const { perSession } = reducePerSession(seed, { type: 'claude_exited', sessionID: 'A' }, b64decode)
    expect(perSession.get('A')?.claude?.turnsLoaded).toBe(false)
  })

  it('claude_exited clears templateId', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui', templateId: 'summary-todo' }],
    ])
    const { perSession } = reducePerSession(seed, { type: 'claude_exited', sessionID: 'A' }, b64decode)
    expect(perSession.get('A')?.templateId).toBeUndefined()
  })

  it('claude_run_ended finalizes an open turn (runner died without result)', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: beginClaudeTurn(emptyClaudeState(), 'hi') }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'claude_run_ended', sessionID: 'A', message: 'signal: interrupt' },
      b64decode,
    )
    const c = perSession.get('A')!.claude!
    expect(c.inFlight).toBe(false)
    expect(c.turns[0].done).toBe(true)
    expect(c.turns[0].isError).toBe(true)
    expect(turnText(c.turns[0])).toBe('signal: interrupt')
  })

  it('claude_run_ended after a normal result is a no-op on turn state', () => {
    // Simulate: beginTurn → result event marks done → run_ended arrives last
    let c = beginClaudeTurn(emptyClaudeState(), 'hi')
    c = applyClaudeEvent(c, 'text_delta', { text: 'hello' })
    c = applyClaudeEvent(c, 'result', { is_error: false, total_cost_usd: 0.001, result: 'hello' })
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: c }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'claude_run_ended', sessionID: 'A' },
      b64decode,
    )
    const out = perSession.get('A')!.claude!
    expect(out.inFlight).toBe(false)
    expect(out.turns[0].done).toBe(true)
    expect(out.turns[0].isError).toBe(false) // unchanged
    expect(turnText(out.turns[0])).toBe('hello')  // unchanged
  })

  it('claude_run_ended with no claude state is a no-op', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', emptyPerSessionState()],
    ])
    const r = reducePerSession(
      seed,
      { type: 'claude_run_ended', sessionID: 'A' },
      b64decode,
    )
    expect(r.perSession).toBe(seed)
  })

  it('finalizeInFlightTurn surfaces the error reason in the empty turn text', () => {
    const c = beginClaudeTurn(emptyClaudeState(), 'hi')
    const out = finalizeInFlightTurn(c, 'boom')
    expect(out.inFlight).toBe(false)
    expect(turnText(out.turns[0])).toBe('boom')
    expect(out.turns[0].isError).toBe(true)
  })

  it('finalizeInFlightTurn does not overwrite existing text', () => {
    let c = beginClaudeTurn(emptyClaudeState(), 'hi')
    c = applyClaudeEvent(c, 'text_delta', { text: 'partial answer' })
    const out = finalizeInFlightTurn(c, 'crash')
    expect(turnText(out.turns[0])).toBe('partial answer')
    expect(out.turns[0].isError).toBe(true)
  })

  it('finalizeInFlightTurn drops pending approvals', () => {
    const c = {
      ...beginClaudeTurn(emptyClaudeState(), 'hi'),
      pending: [{ toolUseId: 't1', tool: 'Bash', input: {} }],
    }
    const out = finalizeInFlightTurn(c)
    expect(out.pending).toEqual([])
  })

  describe('AskUserQuestion routing', () => {
    const askInput = {
      questions: [
        {
          question: 'What would you like to work on?',
          header: 'Task',
          multiSelect: false,
          options: [
            { label: 'Review changes', description: 'Look at recent commits' },
            { label: 'Start feature', description: 'Plan a new feature' },
          ],
        },
      ],
    }

    it('AskUserQuestion tool_approval_request goes to pendingQuestions, not pending', () => {
      const seed = new Map<string, PerSessionState>([
        ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: emptyClaudeState() }],
      ])
      const { perSession } = reducePerSession(
        seed,
        { type: 'tool_approval_request', sessionID: 'A', toolUseId: 'aq1', tool: 'AskUserQuestion', toolInput: askInput },
        b64decode,
      )
      const c = perSession.get('A')!.claude!
      expect(c.pending).toEqual([])
      expect(c.pendingQuestions).toHaveLength(1)
      expect(c.pendingQuestions[0].toolUseId).toBe('aq1')
      expect(c.pendingQuestions[0].questions[0].question).toBe('What would you like to work on?')
      expect(c.pendingQuestions[0].questions[0].options).toHaveLength(2)
    })

    it('AskUserQuestion with malformed input falls back to generic approval', () => {
      const seed = new Map<string, PerSessionState>([
        ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: emptyClaudeState() }],
      ])
      const { perSession } = reducePerSession(
        seed,
        { type: 'tool_approval_request', sessionID: 'A', toolUseId: 'aq2', tool: 'AskUserQuestion', toolInput: { not_questions: 1 } },
        b64decode,
      )
      const c = perSession.get('A')!.claude!
      expect(c.pendingQuestions).toEqual([])
      expect(c.pending).toHaveLength(1)
    })

    it('AskUserQuestion is de-duped on repeat (StrictMode safe)', () => {
      const seed = new Map<string, PerSessionState>([
        ['A', { ...emptyPerSessionState(), renderer: 'ui', claude: { ...emptyClaudeState(), pendingQuestions: [{ toolUseId: 'aq1', questions: [] }] } }],
      ])
      const r = reducePerSession(
        seed,
        { type: 'tool_approval_request', sessionID: 'A', toolUseId: 'aq1', tool: 'AskUserQuestion', toolInput: askInput },
        b64decode,
      )
      expect(r.perSession).toBe(seed)
    })

    it('claude_exited clears pendingQuestions', () => {
      const seed = new Map<string, PerSessionState>([
        ['A', { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui', claude: {
          ...emptyClaudeState(),
          pendingQuestions: [{ toolUseId: 'aq1', questions: [] }],
        } }],
      ])
      const { perSession } = reducePerSession(seed, { type: 'claude_exited', sessionID: 'A' }, b64decode)
      expect(perSession.get('A')!.claude!.pendingQuestions).toEqual([])
    })
  })
})

describe('summary_updated', () => {
  it('bumps summaryFetchCounter for known session', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState() }],
    ])
    const r1 = reducePerSession(seed, { type: 'summary_updated', sessionID: 'A' }, b64decode)
    expect(r1.perSession.get('A')?.summaryFetchCounter).toBe(1)
    const r2 = reducePerSession(r1.perSession, { type: 'summary_updated', sessionID: 'A' }, b64decode)
    expect(r2.perSession.get('A')?.summaryFetchCounter).toBe(2)
  })

  it('ignores summary_updated for unknown session', () => {
    const seed = new Map<string, PerSessionState>()
    const { perSession } = reducePerSession(seed, { type: 'summary_updated', sessionID: 'A' }, b64decode)
    expect(perSession.size).toBe(0)
  })
})
