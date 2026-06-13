import { describe, it, expect } from 'vitest'
import { reducePerSession, applyAuthoritativeRecord } from './sessionsReducer'
import { PerSessionState, emptyPerSessionState } from './types'

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
