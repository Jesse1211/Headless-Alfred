import { ServerMsg } from '../../lib/ws'
import { PerSessionState, emptyPerSessionState, CompletedMsg } from './types'

export interface ReduceResult {
  perSession: Map<string, PerSessionState>
  // Effect to run AFTER the state update — caller decides if it actually
  // fires the fetch (avoids the reducer being impure).
  fetchCommandForSession?: { sessionID: string; cmdID: string }
}

export function reducePerSession(
  prev: Map<string, PerSessionState>,
  m: ServerMsg,
  b64decode: (s: string) => string,
): ReduceResult {
  switch (m.type) {
    case 'idle': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      next.set(m.sessionID, {
        ...cur,
        running: null,
        mode: m.mode ?? cur.mode,
      })
      return { perSession: next }
    }
    case 'reattach': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      next.set(m.sessionID, {
        ...cur,
        running: {
          id: m.cmdId,
          command: m.command,
          startedAt: m.startedAt,
          output: b64decode(m.outputSoFar),
          truncatedLossWarned: false,
        },
        mode: m.mode ?? cur.mode,
      })
      return { perSession: next }
    }
    case 'claude_entered': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      next.set(m.sessionID, { ...cur, mode: 'claude' })
      return { perSession: next }
    }
    case 'claude_exited': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      next.set(m.sessionID, { ...cur, mode: 'shell' })
      return { perSession: next }
    }
    case 'started': {
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
      return { perSession: next }
    }
    case 'chunk': {
      const cur = prev.get(m.sessionID)
      if (!cur || !cur.running || cur.running.id !== m.cmdId) return { perSession: prev }
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        running: { ...cur.running, output: cur.running.output + b64decode(m.data) },
      })
      return { perSession: next }
    }
    case 'done': {
      const cur = prev.get(m.sessionID)
      if (!cur || !cur.running || cur.running.id !== m.cmdId) return { perSession: prev }
      if (cur.messages.some((mm) => mm.id === m.cmdId)) return { perSession: prev }
      const completed: CompletedMsg = {
        id: m.cmdId,
        command: cur.running.command,
        output: cur.running.output,
        startedAt: cur.running.startedAt,
        finishedAt: m.finishedAt,
        exitCode: m.exitCode,
        // Spec: status='completed' for any natural finish, regardless of exit
        // code. Exit code is the field that distinguishes success/failure.
        status: 'completed',
        truncated: false,
      }
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        running: null,
        messages: [...cur.messages, completed],
      })
      return {
        perSession: next,
        fetchCommandForSession: { sessionID: m.sessionID, cmdID: m.cmdId },
      }
    }
    default:
      return { perSession: prev }
  }
}

// Apply the authoritative server record on top of an existing completed message.
export function applyAuthoritativeRecord(
  prev: Map<string, PerSessionState>,
  sessionID: string,
  full: {
    id: string
    command: string
    output: string
    started_at: string
    finished_at?: string
    exit_code?: number
    status: CompletedMsg['status']
    output_truncated: boolean
  },
): Map<string, PerSessionState> {
  const cur = prev.get(sessionID)
  if (!cur) return prev
  const idx = cur.messages.findIndex((mm) => mm.id === full.id)
  if (idx < 0) return prev
  const updated = [...cur.messages]
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
  const next = new Map(prev)
  next.set(sessionID, { ...cur, messages: updated })
  return next
}
