import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ShellSocket, ServerMsg, ConnState, DiskUsage } from '../../lib/ws'
import {
  Session,
  listSessions,
  getCommand,
  createSession as apiCreateSession,
  renameSession as apiRenameSession,
  deleteSession as apiDeleteSession,
  stopCommand as apiStopCommand,
  createRecapSession as apiCreateRecapSession,
  getSession as apiGetSession,
} from '../../lib/api'
import {
  PerSessionState, CompletedMsg, RunningCmd,
  emptyPerSessionState, emptyClaudeState,
} from './types'
import {
  reducePerSession, applyAuthoritativeRecord,
  beginClaudeTurn, resolveClaudeTool, resolveClaudeQuestion, finalizeInFlightTurn,
} from './sessionsReducer'

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
  const [recapFetchCounter, setRecapFetchCounter] = useState(0)
  // Latest PVC capacity snapshot from the disk_usage WS frame.
  // Null until the first frame arrives (backend pushes one
  // immediately on subscribe, but pre-first-frame UI shouldn't
  // render anything).
  const [diskUsage, setDiskUsage] = useState<DiskUsage | null>(null)

  const tokenRef = useRef(token)
  tokenRef.current = token
  const perSessionRef = useRef(perSession)
  perSessionRef.current = perSession
  const sessionsRef = useRef(sessions)
  sessionsRef.current = sessions

  // pty_data dispatch — claude-mode raw PTY bytes go straight to xterm
  // without round-tripping through React state. ClaudeTerminal calls
  // registerPtyHandler(sid, cb) on mount, returns the unregister fn.
  const ptyHandlers = useRef<Map<string, (bytes: Uint8Array) => void>>(new Map())
  const registerPtyHandler = useCallback(
    (sid: string, cb: (bytes: Uint8Array) => void) => {
      ptyHandlers.current.set(sid, cb)
      return () => {
        if (ptyHandlers.current.get(sid) === cb) {
          ptyHandlers.current.delete(sid)
        }
      }
    },
    [],
  )

  // setSessionMeta replaces or inserts a Session meta in the local list.
  // Used after createOrEnterRecap when the new session isn't in the
  // chat-filtered list, and after getSession() rehydration on selection.
  const setSessionMeta = useCallback((s: Session) => {
    setSessions((prev) => {
      const idx = prev.findIndex((x) => x.id === s.id)
      if (idx >= 0) {
        const next = [...prev]
        next[idx] = s
        return next
      }
      return [...prev, s]
    })
  }, [])

  // Initial REST fetch.
  useEffect(() => {
    let alive = true
    listSessions()
      .then(async (list) => {
        if (!alive) return
        setSessions(list)
        const stored = localStorage.getItem(STORAGE_KEY)
        let pick: Session | undefined = list.find((s) => s.id === stored)
        if (!pick && stored) {
          // Maybe the selection is a recap session (not in chat list).
          // Fetch it explicitly; tolerate 404 (session was deleted).
          try {
            const single = await apiGetSession(stored)
            setSessionMeta(single)
            pick = single
          } catch {
            // 404 or other — fall through to default selection
          }
        }
        if (!pick) pick = list[0]
        if (!alive) return
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
          // session_closed / session_renamed / error live outside perSession.
          if (m.type === 'session_closed') {
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
            return
          }
          if (m.type === 'session_renamed') {
            setSessions((prev) => prev.map((s) => (s.id === m.sessionID ? { ...s, name: m.name } : s)))
            return
          }
          if (m.type === 'recap_updated') {
            setRecapFetchCounter((c) => c + 1)
            return
          }
          if (m.type === 'disk_usage') {
            setDiskUsage(m.diskUsage)
            return
          }
          if (m.type === 'error') {
            setLastError({ code: m.code, message: m.message })
            // If the error is tied to a specific session that has an
            // in-flight claude turn (e.g., `busy` / `renderer_mismatch`
            // / `claude_spawn_failed`), finalize that turn so the
            // optimistic beginClaudeTurn doesn't leave the composer
            // locked. Errors with no sessionID are global and don't
            // touch per-session state.
            if (m.sessionID) {
              setPerSession((prev) => {
                const cur = prev.get(m.sessionID!)
                if (!cur || !cur.claude || !cur.claude.inFlight) return prev
                const next = new Map(prev)
                next.set(m.sessionID!, {
                  ...cur,
                  claude: finalizeInFlightTurn(cur.claude, m.message || m.code),
                })
                return next
              })
            }
            return
          }
          if (m.type === 'pong') return
          // Raw PTY bytes (claude mode) bypass React state — they go
          // straight to xterm.write via the registered handler.
          if (m.type === 'pty_data') {
            const cb = ptyHandlers.current.get(m.sessionID)
            if (cb) {
              const bin = atob(m.data)
              const bytes = new Uint8Array(bin.length)
              for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
              cb(bytes)
            }
            return
          }

          // Per-session state mutations go through the reducer.
          setPerSession((prev) => {
            const { perSession: next, fetchCommandForSession } = reducePerSession(prev, m, b64decode)
            if (fetchCommandForSession) {
              const { sessionID, cmdID } = fetchCommandForSession
              const fetchCmd = getCommand(sessionID, cmdID)
              if (fetchCmd && typeof fetchCmd.then === 'function') {
                fetchCmd
                  .then((full) => {
                    setPerSession((prev2) => applyAuthoritativeRecord(prev2, sessionID, full))
                  })
                  .catch(() => {})
              }
            }
            return next
          })
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
      // eslint-disable-next-line no-console
      console.error('createSession failed', e)
      setLastError({
        code: e?.code ?? 'create_failed',
        message: e?.message ? `${e.message} (status ${e.status ?? '?'})` : 'failed',
      })
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

  const enterClaude = useCallback(
    (sid: string, renderer?: 'tui' | 'ui', bypassPermissions?: boolean, templateId?: string) => {
      setPerSession((prev) => {
        const next = new Map(prev)
        const cur = next.get(sid) ?? emptyPerSessionState()
        next.set(sid, { ...cur, templateId: templateId || undefined })
        return next
      })
      socket.send({ type: 'enter_claude', sessionID: sid, renderer, bypassPermissions, templateId })
    },
    [socket],
  )

  const exitClaude = useCallback(
    (sid: string) => {
      socket.send({ type: 'exit_claude', sessionID: sid })
    },
    [socket],
  )

  const sendStdin = useCallback(
    (sid: string, bytes: Uint8Array) => {
      // base64 encode the raw bytes.
      let bin = ''
      for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
      socket.send({ type: 'stdin', sessionID: sid, data: btoa(bin) })
    },
    [socket],
  )

  const claudePrompt = useCallback(
    (sid: string, text: string, opts?: { renderTemplate?: string; optimisticLabel?: string }) => {
      // Optimistic: register the user's prompt as the start of a new
      // turn locally so the chat view renders it immediately. When the
      // text is empty (server-rendered template), use optimisticLabel
      // so the user sees something other than a blank prompt bubble.
      const label = text || opts?.optimisticLabel || ''
      setPerSession((prev) => {
        const next = new Map(prev)
        const cur = next.get(sid) ?? emptyPerSessionState()
        const c = cur.claude ?? emptyClaudeState()
        next.set(sid, { ...cur, claude: beginClaudeTurn(c, label) })
        return next
      })
      socket.send({ type: 'claude_prompt', sessionID: sid, text, renderTemplate: opts?.renderTemplate })
    },
    [socket],
  )

  const toolDecision = useCallback(
    (sid: string, toolUseId: string, decision: 'allow' | 'deny', reason?: string) => {
      setPerSession((prev) => {
        const next = new Map(prev)
        const cur = next.get(sid) ?? emptyPerSessionState()
        if (cur.claude) {
          next.set(sid, { ...cur, claude: resolveClaudeTool(cur.claude, toolUseId, decision) })
        }
        return next
      })
      socket.send({ type: 'tool_decision', sessionID: sid, toolUseId, decision, reason })
    },
    [socket],
  )

  const interruptClaude = useCallback(
    (sid: string) => {
      socket.send({ type: 'interrupt', sessionID: sid })
    },
    [socket],
  )

  // submitQuestionAnswer wires the AskUserQuestion path. The user's
  // selection is shipped back as the `reason` of a tool_decision
  // deny — the PreToolUse hook then surfaces it as the tool's
  // tool_result so Claude sees the answer on the next turn.
  // Optimistically removes the question from pendingQuestions; if
  // the backend rejects, the user sees nothing happen (acceptable
  // since the WS write is local and almost always succeeds).
  const submitQuestionAnswer = useCallback(
    (sid: string, toolUseId: string, answer: string) => {
      setPerSession((prev) => {
        const next = new Map(prev)
        const cur = next.get(sid) ?? emptyPerSessionState()
        if (cur.claude) {
          next.set(sid, { ...cur, claude: resolveClaudeQuestion(cur.claude, toolUseId) })
        }
        return next
      })
      socket.send({ type: 'tool_decision', sessionID: sid, toolUseId, decision: 'deny', reason: answer })
    },
    [socket],
  )

  const clearError = useCallback(() => setLastError(null), [])

  const createOrEnterRecap = useCallback(async () => {
    try {
      const s = await apiCreateRecapSession()
      setSessionMeta(s)
      selectSession(s.id)
      // No StartClaudeDialog: backend has already entered Claude UI mode
      // with bypassPermissions=true. We send an explicit enter_claude here
      // to ensure the perSession state knows it's in claude mode immediately,
      // even before the WS 'idle' frame arrives.
      socket.send({
        type: 'enter_claude',
        sessionID: s.id,
        renderer: 'ui',
        bypassPermissions: true,
        templateId: '', // recap doesn't use the summary template
      })
    } catch (e: any) {
      setLastError({ code: e?.code ?? 'recap_create_failed', message: e?.message ?? 'failed' })
    }
  }, [socket, selectSession, setSessionMeta])

  // Recap session is a singleton: once created, it lives in the
  // backend until alfred-server restarts. Switching away does NOT
  // kill it — we want the same tmux pane, the same Alfred sessionID,
  // and especially the same ClaudeSessionID (=> same on-disk Claude
  // transcript) so the next "Recap" click resumes the conversation
  // with full context. The backend's CreateOrGetRecapSession returns
  // the existing session if it's alive; that's the find-or-create
  // path on subsequent clicks.

  return {
    connState, sessions, selectedSessionID, selectSession, perSession, setPerSession,
    submit, stop, createSession, renameSession, closeSession,
    enterClaude, exitClaude, sendStdin, registerPtyHandler,
    claudePrompt, toolDecision, interruptClaude, submitQuestionAnswer,
    lastError, clearError,
    recapFetchCounter, createOrEnterRecap, setSessionMeta,
    diskUsage,
  }
}

// Re-export types so consumers can import from one place.
export type { Session, PerSessionState, CompletedMsg, RunningCmd }
