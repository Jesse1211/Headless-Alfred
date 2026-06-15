import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useClaudeHistoryLoader } from './useClaudeHistoryLoader'
import { PerSessionState, emptyPerSessionState, emptyClaudeState, ClaudeTurn } from './types'
import * as api from '../../lib/api'

function makeTurn(id: string, prompt: string): ClaudeTurn {
  return { id, prompt, startedAt: '2026-06-15T00:00:00Z', text: 'r', tools: [], done: true }
}

describe('useClaudeHistoryLoader', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('fetches history once for claude+ui session and seeds turns', async () => {
    vi.spyOn(api, 'getClaudeHistory').mockResolvedValue([makeTurn('t1', 'hi')])
    const initial = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui', claude: emptyClaudeState() }],
    ])
    let state = initial
    const setState = vi.fn((updater: (p: typeof state) => typeof state) => {
      state = updater(state)
    })

    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: setState as never,
      }),
    )

    await waitFor(() => {
      expect(state.get('A')?.claude?.turnsLoaded).toBe(true)
    })
    expect(state.get('A')?.claude?.turns).toEqual([makeTurn('t1', 'hi')])
    expect(api.getClaudeHistory).toHaveBeenCalledOnce()
    expect(api.getClaudeHistory).toHaveBeenCalledWith('A')
  })

  it('does not fetch for shell-mode sessions', () => {
    const spy = vi.spyOn(api, 'getClaudeHistory').mockResolvedValue([])
    const state = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), mode: 'shell' }],
    ])
    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: vi.fn() as never,
      }),
    )
    expect(spy).not.toHaveBeenCalled()
  })

  it('does not refetch if turnsLoaded already true', () => {
    const spy = vi.spyOn(api, 'getClaudeHistory').mockResolvedValue([])
    const state = new Map<string, PerSessionState>([
      ['A', {
        ...emptyPerSessionState(),
        mode: 'claude',
        renderer: 'ui',
        claude: { ...emptyClaudeState(), turnsLoaded: true },
      }],
    ])
    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: vi.fn() as never,
      }),
    )
    expect(spy).not.toHaveBeenCalled()
  })

  it('on fetch failure still sets turnsLoaded to true (no retry loop)', async () => {
    vi.spyOn(api, 'getClaudeHistory').mockRejectedValue(new Error('boom'))
    let state = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui', claude: emptyClaudeState() }],
    ])
    const setState = vi.fn((updater: (p: typeof state) => typeof state) => {
      state = updater(state)
    })
    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: setState as never,
      }),
    )
    await waitFor(() => {
      expect(state.get('A')?.claude?.turnsLoaded).toBe(true)
    })
    expect(state.get('A')?.claude?.lastError?.code).toBe('history_unavailable')
  })
})
