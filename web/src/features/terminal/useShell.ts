import { useEffect, useMemo, useRef, useState, useCallback } from 'react'
import { ShellSocket, ServerMsg, ConnState } from '../../lib/ws'
import { stopCommand } from '../../lib/api'
import { RunningCmd } from './types'

function b64decode(s: string): string {
  if (typeof atob === 'function') {
    try {
      const bin = atob(s)
      const bytes = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
      return new TextDecoder().decode(bytes)
    } catch {
      return ''
    }
  }
  return Buffer.from(s, 'base64').toString('utf8')
}

export function useShell(token: string) {
  const [connState, setConnState] = useState<ConnState>('connecting')
  const [running, setRunning] = useState<RunningCmd | null>(null)
  const [idle, setIdle] = useState(false)
  const [lastError, setLastError] = useState<{ code: string; message: string } | null>(null)
  const [historyVersion, setHistoryVersion] = useState(0)

  const tokenRef = useRef(token)
  tokenRef.current = token

  const socket = useMemo(
    () =>
      new ShellSocket({
        url: location.protocol === 'https:' ? `wss://${location.host}/ws` : `ws://${location.host}/ws`,
        getToken: () => tokenRef.current,
        onState: setConnState,
        onMessage: (m: ServerMsg) => {
          switch (m.type) {
            case 'idle':
              setRunning(null)
              setIdle(true)
              break
            case 'reattach':
              setRunning({
                id: m.cmdId,
                command: m.command,
                startedAt: m.startedAt,
                output: b64decode(m.outputSoFar),
                truncatedLossWarned: false,
              })
              setIdle(false)
              break
            case 'started':
              setRunning({
                id: m.cmdId,
                command: m.command,
                startedAt: m.startedAt,
                output: '',
                truncatedLossWarned: false,
              })
              setIdle(false)
              break
            case 'chunk':
              setRunning((cur) => {
                if (!cur || cur.id !== m.cmdId) return cur
                return { ...cur, output: cur.output + b64decode(m.data) }
              })
              break
            case 'done':
              setRunning(null)
              setIdle(true)
              setHistoryVersion((v) => v + 1)
              break
            case 'error':
              setLastError({ code: m.code, message: m.message })
              break
            case 'pong':
              break
          }
        },
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  )

  useEffect(() => {
    socket.start()
    return () => socket.stop()
  }, [socket])

  const submit = useCallback(
    (command: string) => {
      setLastError(null)
      socket.send({ type: 'run', command })
    },
    [socket],
  )

  const stop = useCallback(async (id: string) => {
    try {
      await stopCommand(id)
    } catch {
      // ignore; UI will see error toast separately if it matters
    }
  }, [])

  const clearError = useCallback(() => setLastError(null), [])

  return { connState, running, idle, lastError, clearError, submit, stop, historyVersion }
}
