import { renderHook, waitFor, act } from '@testing-library/react'
import { useSessions } from './useSessions'
import { describe, it, expect, beforeEach, vi } from 'vitest'

// Mock the lib/api module so we don't make real HTTP calls.
vi.mock('../../lib/api', () => {
  return {
    listSessions: vi.fn(() => Promise.resolve([
      { id: 'sess-A', name: 'A', created_at: '2026-06-11T00:00:00Z' },
      { id: 'sess-B', name: 'B', created_at: '2026-06-11T00:01:00Z' },
    ])),
    listCommands: vi.fn(() => Promise.resolve([])),
    getCommand: vi.fn(),
    createSession: vi.fn((n) =>
      Promise.resolve({ id: 'sess-NEW', name: n || 'Session 3', created_at: 'now' }),
    ),
    renameSession: vi.fn(() => Promise.resolve()),
    deleteSession: vi.fn(() => Promise.resolve()),
    stopCommand: vi.fn(),
  }
})

// Mock the ShellSocket. The hook never actually opens a real socket.
let _onMessage: ((m: any) => void) | null = null
const sendMock = vi.fn()
vi.mock('../../lib/ws', () => {
  return {
    ShellSocket: vi.fn().mockImplementation((opts: any) => {
      _onMessage = opts.onMessage
      opts.onState('open')
      return { start: vi.fn(), stop: vi.fn(), send: sendMock }
    }),
  }
})

beforeEach(() => {
  localStorage.clear()
  sendMock.mockClear()
  _onMessage = null
})

describe('useSessions — initial load', () => {
  it('loads sessions from the API on mount', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    expect(result.current.sessions[0].id).toBe('sess-A')
  })

  it('selects the first session when nothing in localStorage', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-A'))
  })

  it('rehydrates selectedSessionID from localStorage if it still exists', async () => {
    localStorage.setItem('alfred_selected_session', 'sess-B')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-B'))
  })

  it('falls back to first session if stored ID is unknown', async () => {
    localStorage.setItem('alfred_selected_session', 'sess-GHOST')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-A'))
  })

  it('selectSession persists to localStorage', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => result.current.selectSession('sess-B'))
    expect(result.current.selectedSessionID).toBe('sess-B')
    expect(localStorage.getItem('alfred_selected_session')).toBe('sess-B')
  })
})
