import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ShellSocket, ServerMsg, ConnState } from '../../lib/ws'
import {
  Session,
  listSessions,
  listCommands,
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

  // WS plumbed in Task 5; this skeleton just opens a no-op socket.
  const socket = useMemo(
    () =>
      new ShellSocket({
        url: location.protocol === 'https:' ? `wss://${location.host}/ws` : `ws://${location.host}/ws`,
        getToken: () => tokenRef.current,
        onState: setConnState,
        onMessage: (_m: ServerMsg) => {
          // Task 5 fills this in.
        },
      }),
    [],
  )

  useEffect(() => {
    socket.start()
    return () => socket.stop()
  }, [socket])

  const clearError = useCallback(() => setLastError(null), [])

  return {
    connState,
    sessions,
    selectedSessionID,
    selectSession,
    perSession,
    lastError,
    clearError,
    // Placeholders so consumers compile during Task 5:
    submit: (_cmd: string) => {},
    stop: (_cmdID: string) => {},
    createSession: async (_name?: string) => null as Session | null,
    renameSession: async (_id: string, _name: string) => {},
    closeSession: async (_id: string) => {},
  }
}

// Re-export types so consumers can import from one place.
export type { Session, PerSessionState, CompletedMsg, RunningCmd }
