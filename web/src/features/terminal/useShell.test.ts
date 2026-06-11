import { renderHook, act, waitFor } from '@testing-library/react'
import { useShell } from './useShell'
import { describe, it, expect, beforeEach, vi } from 'vitest'

const sendMock = vi.fn()

let onMessage: ((m: any) => void) | null = null
let onState: ((s: any) => void) | null = null

vi.mock('../../lib/ws', () => {
  return {
    ShellSocket: vi.fn().mockImplementation((opts: any) => {
      onMessage = opts.onMessage
      onState = opts.onState
      return {
        start: vi.fn(() => onState!('open')),
        stop: vi.fn(),
        send: sendMock,
      }
    }),
  }
})

function b64(s: string): string {
  return Buffer.from(s, 'utf8').toString('base64')
}

describe('useShell', () => {
  beforeEach(() => {
    sendMock.mockClear()
    onMessage = null
    onState = null
  })

  it('starts in connecting then open', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(result.current.connState).toBe('open'))
  })

  it('handles idle message: running is null, idle=true', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'idle' }))
    expect(result.current.running).toBeNull()
    expect(result.current.idle).toBe(true)
  })

  it('handles started + chunk + done', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'started', cmdId: 'X', command: 'ls', startedAt: '2026-06-11T00:00:00Z' }))
    expect(result.current.running?.id).toBe('X')

    act(() => onMessage!({ type: 'chunk', cmdId: 'X', data: b64('hello\n') }))
    expect(result.current.running?.output).toBe('hello\n')

    act(() => onMessage!({ type: 'chunk', cmdId: 'X', data: b64('world\n') }))
    expect(result.current.running?.output).toBe('hello\nworld\n')

    act(() => onMessage!({ type: 'done', cmdId: 'X', exitCode: 0, finishedAt: '2026-06-11T00:00:01Z' }))
    expect(result.current.running).toBeNull()
    expect(result.current.idle).toBe(true)
  })

  it('handles reattach', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() =>
      onMessage!({
        type: 'reattach',
        cmdId: 'Y',
        command: 'train',
        startedAt: '2026-06-11T00:00:00Z',
        outputSoFar: b64('epoch 1\n'),
      }),
    )
    expect(result.current.running?.id).toBe('Y')
    expect(result.current.running?.output).toBe('epoch 1\n')
  })

  it('submit calls send', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'idle' }))
    act(() => result.current.submit('ls -la'))
    expect(sendMock).toHaveBeenCalledWith({ type: 'run', command: 'ls -la' })
  })

  it('busy error sets lastError', async () => {
    const { result } = renderHook(() => useShell('TOK'))
    await waitFor(() => expect(onMessage).not.toBeNull())
    act(() => onMessage!({ type: 'error', code: 'busy', message: 'shell busy' }))
    expect(result.current.lastError?.code).toBe('busy')
  })
})
