import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useClaudeStateLoader } from './useClaudeStateLoader'
import { emptyClaudeState, emptyPerSessionState, PerSessionState, ClaudeState } from './types'
import * as api from '../../lib/api'

function fakeState(turnID = 'u1'): ClaudeState {
  return {
    ...emptyClaudeState(),
    turns: [{
      id: turnID, prompt: 'hi', startedAt: '2026-06-18T07:00:00Z',
      blocks: [], done: true,
    }],
    turnsLoaded: true,
  }
}

describe('useClaudeStateLoader', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('hydrates the perSession map with server state on first claude entry', async () => {
    vi.spyOn(api, 'getClaudeState').mockResolvedValue(fakeState('u1'))
    const sid = 'sess1'
    let state = new Map<string, PerSessionState>([
      [sid, { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui' }],
    ])
    const setState = vi.fn((updater: (p: typeof state) => typeof state) => {
      state = updater(state)
    })
    renderHook(() =>
      useClaudeStateLoader({
        selectedSessionID: sid,
        perSession: state,
        setPerSession: setState as never,
      })
    )

    await waitFor(() => {
      expect(state.get(sid)?.claude?.turnsLoaded).toBe(true)
    })
    expect(state.get(sid)?.claude?.turns).toHaveLength(1)
    expect(state.get(sid)?.claude?.turns[0].id).toBe('u1')
  })

  it('skips fetch when turnsLoaded is already true', async () => {
    const fetch = vi.spyOn(api, 'getClaudeState').mockResolvedValue(fakeState())
    const sid = 'sess1'
    const state = new Map<string, PerSessionState>([
      [sid, {
        ...emptyPerSessionState(),
        mode: 'claude', renderer: 'ui',
        claude: { ...emptyClaudeState(), turnsLoaded: true },
      }],
    ])
    renderHook(() =>
      useClaudeStateLoader({
        selectedSessionID: sid,
        perSession: state,
        setPerSession: vi.fn() as never,
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
    let state = new Map<string, PerSessionState>([
      [sid, {
        ...emptyPerSessionState(),
        mode: 'claude', renderer: 'ui', claude: existing,
      }],
    ])
    const setState = vi.fn((updater: (p: typeof state) => typeof state) => {
      state = updater(state)
    })
    renderHook(() =>
      useClaudeStateLoader({
        selectedSessionID: sid,
        perSession: state,
        setPerSession: setState as never,
      })
    )
    await waitFor(() => expect(setState).toHaveBeenCalled())
    const c = state.get(sid)!.claude!
    expect(c.turns).toHaveLength(1)
    expect(c.turns[0].id).toBe('kept') // existing preserved
    expect(c.lastError?.code).toBe('history_unavailable')
  })

  it('does not fetch for shell-mode sessions', () => {
    const fetch = vi.spyOn(api, 'getClaudeState').mockResolvedValue(fakeState())
    const sid = 'sess1'
    const state = new Map<string, PerSessionState>([
      [sid, { ...emptyPerSessionState(), mode: 'shell' }],
    ])
    renderHook(() =>
      useClaudeStateLoader({
        selectedSessionID: sid,
        perSession: state,
        setPerSession: vi.fn() as never,
      })
    )
    expect(fetch).not.toHaveBeenCalled()
  })
})
