import { useEffect, useMemo, useRef, useState, useCallback } from 'react'
import { ShellSocket, ServerMsg, ConnState } from '../../lib/ws'
import { stopCommand, listCommands, getCommand } from '../../lib/api'
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

export interface CompletedMsg {
  id: string
  command: string
  output: string
  startedAt: string
  finishedAt?: string
  exitCode?: number
  status: 'completed' | 'interrupted' | 'stopped' | 'running'
  truncated: boolean
}

export function useShell(token: string) {
  const [connState, setConnState] = useState<ConnState>('connecting')
  const [running, setRunning] = useState<RunningCmd | null>(null)
  const [idle, setIdle] = useState(false)
  const [lastError, setLastError] = useState<{ code: string; message: string } | null>(null)
  const [historyVersion, setHistoryVersion] = useState(0)
  const [messages, setMessages] = useState<CompletedMsg[]>([])

  const tokenRef = useRef(token)
  tokenRef.current = token

  // Mirror of `running` so the `done` handler can read the snapshot
  // synchronously without going through a setRunning updater (which React
  // StrictMode invokes twice in dev — turning a single completed command
  // into two appended messages).
  const runningRef = useRef<RunningCmd | null>(null)

  const socket = useMemo(
    () =>
      new ShellSocket({
        url: location.protocol === 'https:' ? `wss://${location.host}/ws` : `ws://${location.host}/ws`,
        getToken: () => tokenRef.current,
        onState: setConnState,
        onMessage: (m: ServerMsg) => {
          switch (m.type) {
            case 'idle':
              runningRef.current = null
              setRunning(null)
              setIdle(true)
              break
            case 'reattach': {
              const next = {
                id: m.cmdId,
                command: m.command,
                startedAt: m.startedAt,
                output: b64decode(m.outputSoFar),
                truncatedLossWarned: false,
              }
              runningRef.current = next
              setRunning(next)
              setIdle(false)
              break
            }
            case 'started': {
              const next = {
                id: m.cmdId,
                command: m.command,
                startedAt: m.startedAt,
                output: '',
                truncatedLossWarned: false,
              }
              runningRef.current = next
              setRunning(next)
              setIdle(false)
              break
            }
            case 'chunk': {
              const cur = runningRef.current
              if (!cur || cur.id !== m.cmdId) break
              const next = { ...cur, output: cur.output + b64decode(m.data) }
              runningRef.current = next
              setRunning(next)
              break
            }
            case 'done': {
              const finishedAt = m.finishedAt
              const exitCode = m.exitCode
              const cmdId = m.cmdId
              // Capture the running snapshot synchronously so the messages
              // updater can be a pure idempotent function of msgs (no closure
              // over a setRunning that React StrictMode would invoke twice).
              const snapshot = runningRef.current
              runningRef.current = null
              setRunning(null)
              setIdle(true)
              setHistoryVersion((v) => v + 1)
              if (snapshot && snapshot.id === cmdId) {
                setMessages((msgs) => {
                  if (msgs.some((mm) => mm.id === snapshot.id)) return msgs
                  return [
                    ...msgs,
                    {
                      id: snapshot.id,
                      command: snapshot.command,
                      output: snapshot.output,
                      startedAt: snapshot.startedAt,
                      finishedAt,
                      exitCode,
                      status: 'completed',
                      truncated: false,
                    },
                  ]
                })
                getCommand(snapshot.id)
                  .then((full) => {
                    setMessages((msgs) =>
                      msgs.map((mm) =>
                        mm.id === full.id
                          ? {
                              id: full.id,
                              command: full.command,
                              output: full.output,
                              startedAt: full.started_at,
                              finishedAt: full.finished_at,
                              exitCode: full.exit_code,
                              status: full.status,
                              truncated: full.output_truncated,
                            }
                          : mm,
                      ),
                    )
                  })
                  .catch(() => {
                    /* keep the provisional entry — better than nothing */
                  })
              }
              break
            }
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

  // Initial load: pull recent history + their full output bodies in parallel.
  // Limited to 50 to keep first paint manageable even if user has thousands of
  // commands with multi-MB outputs.
  useEffect(() => {
    let alive = true
    listCommands({ limit: 50 })
      .then(async (rows) => {
        const sorted = [...rows].sort((a, b) => a.started_at.localeCompare(b.started_at))
        const full = await Promise.all(
          sorted.map((r) => getCommand(r.id).catch(() => null)),
        )
        if (!alive) return
        const msgs: CompletedMsg[] = []
        for (let i = 0; i < sorted.length; i++) {
          const f = full[i]
          if (!f) continue
          msgs.push({
            id: f.id,
            command: f.command,
            output: f.output,
            startedAt: f.started_at,
            finishedAt: f.finished_at,
            exitCode: f.exit_code,
            status: f.status,
            truncated: f.output_truncated,
          })
        }
        setMessages(msgs)
      })
      .catch(() => {
        /* ignore — empty stream is fine */
      })
    return () => {
      alive = false
    }
  }, [])

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

  return { connState, running, idle, lastError, clearError, submit, stop, historyVersion, messages }
}
