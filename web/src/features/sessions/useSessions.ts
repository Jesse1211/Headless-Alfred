import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ShellSocket, ServerMsg, ConnState } from '../../lib/ws'
import {
  Session,
  listSessions,
  getCommand,
  createSession as apiCreateSession,
  renameSession as apiRenameSession,
  deleteSession as apiDeleteSession,
  stopCommand as apiStopCommand,
} from '../../lib/api'
import { PerSessionState, emptyPerSessionState, CompletedMsg, RunningCmd } from './types'

const STORAGE_KEY = 'alfred_selected_session'

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

export function useSessions(token: string) {
  const [connState, setConnState] = useState<ConnState>('connecting')
  const [sessions, setSessions] = useState<Session[]>([])
  const [selectedSessionID, setSelectedSessionID] = useState<string | null>(null)
  const [perSession, setPerSession] = useState<Map<string, PerSessionState>>(new Map())
  const [lastError, setLastError] = useState<{ code: string; message: string } | null>(null)

  const tokenRef = useRef(token)
  tokenRef.current = token
  const perSessionRef = useRef(perSession)
  perSessionRef.current = perSession
  const sessionsRef = useRef(sessions)
  sessionsRef.current = sessions

  // Initial REST fetch.
  useEffect(() => {
    let alive = true
    listSessions()
      .then((list) => {
        if (!alive) return
        setSessions(list)
        // Rehydrate selection.
        const stored = localStorage.getItem(STORAGE_KEY)
        let pick = list.find((s) => s.id === stored)
        if (!pick) pick = list[0]
        setSelectedSessionID(pick?.id ?? null)
        if (pick) localStorage.setItem(STORAGE_KEY, pick.id)
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [])

  const selectSession = useCallback((id: string) => {
    setSelectedSessionID(id)
    localStorage.setItem(STORAGE_KEY, id)
  }, [])

  const socket = useMemo(
    () =>
      new ShellSocket({
        url: location.protocol === 'https:' ? `wss://${location.host}/ws` : `ws://${location.host}/ws`,
        getToken: () => tokenRef.current,
        onState: setConnState,
        onMessage: (m: ServerMsg) => {
          switch (m.type) {
            case 'idle':
              setPerSession((prev) => {
                const next = new Map(prev)
                next.set(m.sessionID, { ...(next.get(m.sessionID) ?? emptyPerSessionState()), running: null })
                return next
              })
              break
            case 'reattach':
              setPerSession((prev) => {
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...(next.get(m.sessionID) ?? emptyPerSessionState()),
                  running: {
                    id: m.cmdId,
                    command: m.command,
                    startedAt: m.startedAt,
                    output: b64decode(m.outputSoFar),
                    truncatedLossWarned: false,
                  },
                })
                return next
              })
              break
            case 'started':
              setPerSession((prev) => {
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...(next.get(m.sessionID) ?? emptyPerSessionState()),
                  running: {
                    id: m.cmdId,
                    command: m.command,
                    startedAt: m.startedAt,
                    output: '',
                    truncatedLossWarned: false,
                  },
                })
                return next
              })
              break
            case 'chunk':
              setPerSession((prev) => {
                const cur = prev.get(m.sessionID)
                if (!cur || !cur.running || cur.running.id !== m.cmdId) return prev
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...cur,
                  running: { ...cur.running, output: cur.running.output + b64decode(m.data) },
                })
                return next
              })
              break
            case 'done':
              setPerSession((prev) => {
                const cur = prev.get(m.sessionID)
                if (!cur || !cur.running || cur.running.id !== m.cmdId) return prev
                if (cur.messages.some((mm) => mm.id === m.cmdId)) return prev
                const completed: CompletedMsg = {
                  id: m.cmdId,
                  command: cur.running.command,
                  output: cur.running.output,
                  startedAt: cur.running.startedAt,
                  finishedAt: m.finishedAt,
                  exitCode: m.exitCode,
                  status: m.exitCode === 0 ? 'completed' : 'completed',
                  truncated: false,
                }
                const next = new Map(prev)
                next.set(m.sessionID, {
                  ...cur,
                  running: null,
                  messages: [...cur.messages, completed],
                })
                // Fire-and-forget: fetch the authoritative record.
                const fetchCmd = getCommand(m.sessionID, m.cmdId)
                if (!fetchCmd || typeof fetchCmd.then !== 'function') return next
                fetchCmd.then((full) => {
                  setPerSession((prev2) => {
                    const cur2 = prev2.get(m.sessionID)
                    if (!cur2) return prev2
                    const idx = cur2.messages.findIndex((mm) => mm.id === full.id)
                    if (idx < 0) return prev2
                    const updated = [...cur2.messages]
                    updated[idx] = {
                      id: full.id,
                      command: full.command,
                      output: full.output,
                      startedAt: full.started_at,
                      finishedAt: full.finished_at,
                      exitCode: full.exit_code,
                      status: full.status,
                      truncated: full.output_truncated,
                    }
                    const next2 = new Map(prev2)
                    next2.set(m.sessionID, { ...cur2, messages: updated })
                    return next2
                  })
                }).catch(() => {})
                return next
              })
              break
            case 'session_closed':
              setSessions((prev) => prev.filter((s) => s.id !== m.sessionID))
              setPerSession((prev) => {
                const next = new Map(prev)
                next.delete(m.sessionID)
                return next
              })
              setSelectedSessionID((prev) => {
                if (prev !== m.sessionID) return prev
                const remaining = sessionsRef.current.filter((s) => s.id !== m.sessionID)
                const next = remaining[0]?.id ?? null
                if (next) localStorage.setItem(STORAGE_KEY, next)
                else localStorage.removeItem(STORAGE_KEY)
                return next
              })
              break
            case 'session_renamed':
              setSessions((prev) => prev.map((s) => (s.id === m.sessionID ? { ...s, name: m.name } : s)))
              break
            case 'error':
              setLastError({ code: m.code, message: m.message })
              break
          }
        },
      }),
    [],
  )

  useEffect(() => {
    socket.start()
    return () => socket.stop()
  }, [socket])

  const submit = useCallback(
    (command: string) => {
      const sid = selectedSessionID
      if (!sid) return
      setLastError(null)
      socket.send({ type: 'run', sessionID: sid, command })
    },
    [socket, selectedSessionID],
  )

  const stop = useCallback(
    async (cmdID: string) => {
      const sid = selectedSessionID
      if (!sid) return
      try { await apiStopCommand(sid, cmdID) } catch {}
    },
    [selectedSessionID],
  )

  const createSession = useCallback(async (name?: string) => {
    try {
      const created = await apiCreateSession(name)
      setSessions((prev) => [...prev, created])
      selectSession(created.id)
      return created
    } catch (e: any) {
      setLastError({ code: e.code ?? 'create_failed', message: e.message ?? 'failed' })
      return null
    }
  }, [selectSession])

  const renameSession = useCallback(async (id: string, name: string) => {
    try {
      await apiRenameSession(id, name)
      setSessions((prev) => prev.map((s) => (s.id === id ? { ...s, name } : s)))
    } catch (e: any) {
      setLastError({ code: e.code ?? 'rename_failed', message: e.message ?? 'failed' })
    }
  }, [])

  const closeSession = useCallback(async (id: string) => {
    try {
      await apiDeleteSession(id)
      // Server will also broadcast session_closed, which removes from state.
      // We don't optimistically remove here to keep cross-tab semantics simple.
    } catch (e: any) {
      setLastError({ code: e.code ?? 'close_failed', message: e.message ?? 'failed' })
    }
  }, [])

  const clearError = useCallback(() => setLastError(null), [])

  return {
    connState, sessions, selectedSessionID, selectSession, perSession, setPerSession,
    submit, stop, createSession, renameSession, closeSession,
    lastError, clearError,
  }
}

// Re-export types so consumers can import from one place.
export type { Session, PerSessionState, CompletedMsg, RunningCmd }
