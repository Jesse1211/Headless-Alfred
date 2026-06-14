import { describe, it, expect } from 'vitest'
import {
  reducePerSession, applyAuthoritativeRecord,
  applyClaudeEvent, beginClaudeTurn, resolveClaudeTool,
} from './sessionsReducer'
import { PerSessionState, emptyPerSessionState, emptyClaudeState } from './types'

const id = (s: string) => Buffer.from(s, 'utf8').toString('base64')
const b64decode = (s: string) => Buffer.from(s, 'base64').toString('utf8')

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

  it('done is idempotent (StrictMode safe)', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), messages: [{ id: 'c1', command: 'ls', output: '', startedAt: 't', finishedAt: 't2', exitCode: 0, status: 'completed', truncated: false }], running: { id: 'c1', command: 'ls', startedAt: 't', output: '', truncatedLossWarned: false } }],
    ])
    const { perSession } = reducePerSession(
      seed,
      { type: 'done', sessionID: 'A', cmdId: 'c1', exitCode: 0, finishedAt: 't2' },
      b64decode,
    )
    expect(perSession).toBe(seed)
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
    expect(turns[0].text).toBe('Hello')
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
        id: 'turn-1', prompt: 'p', startedAt: 't', text: '', tools: [
          { toolUseId: 't1', name: 'Bash', decision: 'pending' as const },
        ], done: false,
      }],
    }
    const next = resolveClaudeTool(c, 't1', 'allow')
    expect(next.pending).toEqual([])
    expect(next.turns[0].tools[0].decision).toBe('allow')
  })

  it('applyClaudeEvent handles tool_use_start, tool_use_end, tool_result in order', () => {
    let c = beginClaudeTurn(emptyClaudeState(), 'do x')
    c = applyClaudeEvent(c, 'tool_use_start', { tool_use_id: 't1', name: 'Bash' })
    expect(c.turns[0].tools[0]).toMatchObject({ toolUseId: 't1', name: 'Bash', decision: 'pending' })
    c = applyClaudeEvent(c, 'tool_use_end', { tool_use_id: 't1', input: { cmd: 'ls' } })
    expect(c.turns[0].tools[0].input).toEqual({ cmd: 'ls' })
    c = applyClaudeEvent(c, 'tool_result', { tool_use_id: 't1', content: 'a\nb', is_error: false })
    expect(c.turns[0].tools[0].result).toBe('a\nb')
    expect(c.turns[0].tools[0].isError).toBe(false)
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
          turns: [{ id: 'turn-1', prompt: 'p', startedAt: 't', text: 'reply', tools: [], done: true }],
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
})
