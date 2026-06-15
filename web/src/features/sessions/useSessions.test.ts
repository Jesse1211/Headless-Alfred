import { renderHook, waitFor, act } from '@testing-library/react'
import { useSessions } from './useSessions'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import * as api from '../../lib/api'

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
    createRecapSession: vi.fn(() =>
      Promise.resolve({ id: 'sess-RECAP', name: 'Recap', kind: 'recap', created_at: 'now' }),
    ),
    deleteRecapSession: vi.fn(() => Promise.resolve()),
    getSession: vi.fn(() => Promise.reject(new Error('not found'))),
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

function b64(s: string): string {
  return Buffer.from(s, 'utf8').toString('base64')
}

describe('useSessions — WS events', () => {
  it('idle for session sets perSession state to empty running', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'idle', sessionID: 'sess-A' }))
    const ps = result.current.perSession.get('sess-A')
    expect(ps?.running).toBeNull()
  })

  it('started/chunk/done update running and append to messages', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() =>
      _onMessage!({
        type: 'started',
        sessionID: 'sess-A',
        cmdId: 'X',
        command: 'ls',
        startedAt: 'now',
      }),
    )
    expect(result.current.perSession.get('sess-A')?.running?.id).toBe('X')

    act(() => _onMessage!({ type: 'chunk', sessionID: 'sess-A', cmdId: 'X', data: b64('hello\n') }))
    expect(result.current.perSession.get('sess-A')?.running?.output).toBe('hello\n')

    act(() =>
      _onMessage!({
        type: 'done',
        sessionID: 'sess-A',
        cmdId: 'X',
        exitCode: 0,
        finishedAt: 'fin',
      }),
    )
    const psA = result.current.perSession.get('sess-A')!
    expect(psA.running).toBeNull()
    expect(psA.messages.length).toBe(1)
    expect(psA.messages[0].id).toBe('X')
  })

  it('chunks for one session do not disturb another sessions running state', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'started', sessionID: 'sess-A', cmdId: 'A1', command: 'a', startedAt: 't' }))
    act(() => _onMessage!({ type: 'started', sessionID: 'sess-B', cmdId: 'B1', command: 'b', startedAt: 't' }))
    act(() => _onMessage!({ type: 'chunk', sessionID: 'sess-A', cmdId: 'A1', data: b64('A-data') }))
    expect(result.current.perSession.get('sess-A')?.running?.output).toBe('A-data')
    expect(result.current.perSession.get('sess-B')?.running?.output).toBe('')
  })

  it('session_closed removes session and clears perSession', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'session_closed', sessionID: 'sess-A' }))
    expect(result.current.sessions.find((s) => s.id === 'sess-A')).toBeUndefined()
    expect(result.current.perSession.get('sess-A')).toBeUndefined()
  })

  it('session_closed reassigns selectedSessionID if it pointed at the closed one', async () => {
    localStorage.setItem('alfred_selected_session', 'sess-A')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.selectedSessionID).toBe('sess-A'))
    act(() => _onMessage!({ type: 'session_closed', sessionID: 'sess-A' }))
    expect(result.current.selectedSessionID).toBe('sess-B')
  })

  it('session_renamed updates the name', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => _onMessage!({ type: 'session_renamed', sessionID: 'sess-A', name: 'training' }))
    expect(result.current.sessions.find((s) => s.id === 'sess-A')?.name).toBe('training')
  })

  it('submit sends run with sessionID', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => result.current.selectSession('sess-B'))
    act(() => result.current.submit('ls -la'))
    expect(sendMock).toHaveBeenCalledWith({ type: 'run', sessionID: 'sess-B', command: 'ls -la' })
  })

  it('enterClaude with templateId writes it to perSession', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => { result.current.enterClaude('sess-A', 'ui', true, 'summary-todo') })
    expect(result.current.perSession.get('sess-A')?.templateId).toBe('summary-todo')
  })

  it('enterClaude without templateId leaves it undefined', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    act(() => { result.current.enterClaude('sess-A', 'ui', true) })
    expect(result.current.perSession.get('sess-A')?.templateId).toBeUndefined()
  })
})

describe('useSessions — recap', () => {
  it('recap_updated frame bumps recapFetchCounter', async () => {
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    const before = result.current.recapFetchCounter
    act(() => _onMessage!({ type: 'recap_updated', date: '2026-06-15' }))
    expect(result.current.recapFetchCounter).toBe(before + 1)
  })

  it('createOrEnterRecap selects the returned session and seeds meta', async () => {
    const createSpy = vi.spyOn(api, 'createRecapSession')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    await act(async () => {
      await result.current.createOrEnterRecap()
    })
    expect(createSpy).toHaveBeenCalled()
    expect(result.current.selectedSessionID).toBe('sess-RECAP')
    const meta = result.current.sessions.find((s) => s.id === 'sess-RECAP')
    expect(meta?.kind).toBe('recap')
    expect(sendMock).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'enter_claude', sessionID: 'sess-RECAP', bypassPermissions: true }),
    )
  })

  it('switch-away from recap session fires deleteRecapSession', async () => {
    const deleteSpy = vi.spyOn(api, 'deleteRecapSession')
    const { result } = renderHook(() => useSessions('TOK'))
    await waitFor(() => expect(result.current.sessions.length).toBe(2))
    // Enter recap session
    await act(async () => {
      await result.current.createOrEnterRecap()
    })
    expect(result.current.selectedSessionID).toBe('sess-RECAP')
    deleteSpy.mockClear()
    // Switch away to a chat session
    act(() => result.current.selectSession('sess-A'))
    // The auto-delete effect fires after the selectedSessionID change
    await waitFor(() => expect(deleteSpy).toHaveBeenCalled())
  })
})
