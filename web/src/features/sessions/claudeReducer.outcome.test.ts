import { describe, it, expect } from 'vitest'
import { finalizeInFlightTurn } from './claudeReducer'
import type { ClaudeState } from './types'

function base(): ClaudeState {
  return {
    turns: [{ id: 'u1', prompt: 'p', startedAt: new Date().toISOString(), blocks: [], done: false }],
    inFlight: true, pending: [], pendingQuestions: [], bgTasks: {}, subagents: {}, bgTaskLogs: {},
  }
}

describe('finalizeInFlightTurn outcome', () => {
  it('marks the in-flight turn aborted', () => {
    const next = finalizeInFlightTurn(base(), 'runner died', '2026-06-20T00:00:00Z')
    expect(next.turns[0].outcome).toBe('aborted')
    expect(next.inFlight).toBe(false)
  })
})
