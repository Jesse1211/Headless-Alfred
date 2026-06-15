import { ServerMsg } from '../../lib/ws'
import {
  PerSessionState,
  emptyPerSessionState,
  CompletedMsg,
  emptyClaudeState,
} from './types'
import { reduceClaudeMsg } from './claudeReducer'

// Re-export Claude helpers so existing imports (`from './sessionsReducer'`)
// keep working — useSessions and the test suite both rely on this path.
export {
  applyClaudeEvent,
  beginClaudeTurn,
  finalizeInFlightTurn,
  resolveClaudeTool,
  resolveClaudeQuestion,
  parseAskUserQuestionInput,
} from './claudeReducer'

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
  // Claude-UI frames go to the dedicated reducer. Returns null if `m`
  // isn't one of its cases so we can fall through to the shell switch.
  const fromClaude = reduceClaudeMsg(prev, m)
  if (fromClaude !== null) return { perSession: fromClaude }

  switch (m.type) {
    case 'idle': {
      const cur = prev.get(m.sessionID) ?? emptyPerSessionState()
      const renderer = (m.renderer ?? cur.renderer ?? '') as PerSessionState['renderer']
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        running: null,
        mode: m.mode ?? cur.mode,
        renderer,
        // If reconnecting and we see we're in UI mode but no claude
        // state yet, initialize it.
        claude: renderer === 'ui' && !cur.claude ? emptyClaudeState() : cur.claude,
      })
      return { perSession: next }
    }
    case 'reattach': {
      const cur = prev.get(m.sessionID) ?? emptyPerSessionState()
      const renderer = (m.renderer ?? cur.renderer ?? '') as PerSessionState['renderer']
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        running: {
          id: m.cmdId,
          command: m.command,
          startedAt: m.startedAt,
          output: b64decode(m.outputSoFar),
          truncatedLossWarned: false,
        },
        renderer,
        claude: renderer === 'ui' && !cur.claude ? emptyClaudeState() : cur.claude,
        mode: m.mode ?? cur.mode,
      })
      return { perSession: next }
    }
    case 'started': {
      const cur = prev.get(m.sessionID) ?? emptyPerSessionState()
      // Defensive de-dup: if this cmdId already finished (it's in
      // messages), ignore the duplicate started. Otherwise a late
      // re-broadcast of started after done would resurrect a phantom
      // running turn that the matching done can no longer clear
      // (done early-returns when messages.has(cmdId) — see below).
      if (cur.messages.some((mm) => mm.id === m.cmdId)) {
        return { perSession: prev }
      }
      // Also a no-op if we already have THIS cmdId as the live running
      // turn — avoids needlessly rebuilding the object and resetting
      // output that chunks have already filled in.
      if (cur.running && cur.running.id === m.cmdId) {
        return { perSession: prev }
      }
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
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
      // If this exact (cmdId, byteLength, prefix) chunk already
      // arrived (duplicated by a re-broadcast), don't append twice.
      // Cheap heuristic: a duplicate would extend output past what
      // the matching message in messages[] eventually shows, which
      // is invisible — but the user sees the running buffer grow
      // wrong RIGHT NOW. We check "have we seen this exact payload
      // as the tail of the buffer just now?". The only false positive
      // is a command that legitimately produces the same bytes twice
      // in a row right at the boundary (e.g. `printf 'aa'`), which is
      // benign in practice.
      const data = b64decode(m.data)
      if (data && cur.running.output.endsWith(data) && data.length > 0) {
        return { perSession: prev }
      }
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        running: { ...cur.running, output: cur.running.output + data },
      })
      return { perSession: next }
    }
    case 'done': {
      const cur = prev.get(m.sessionID)
      if (!cur) return { perSession: prev }
      // Hard idempotence: if we've already recorded this cmdId, we
      // still need to make sure no stale `running` is hanging around
      // with the same id (could happen if a duplicate `started`
      // resurrected it after the original done already moved it).
      // Clear it so the UI doesn't show a phantom live turn.
      if (cur.messages.some((mm) => mm.id === m.cmdId)) {
        if (cur.running && cur.running.id === m.cmdId) {
          const next = new Map(prev)
          next.set(m.sessionID, { ...cur, running: null })
          return { perSession: next }
        }
        return { perSession: prev }
      }
      if (!cur.running || cur.running.id !== m.cmdId) return { perSession: prev }
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
    case 'summary_updated': {
      const cur = prev.get(m.sessionID)
      if (!cur) return { perSession: prev }
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        summaryFetchCounter: (cur.summaryFetchCounter ?? 0) + 1,
      })
      return { perSession: next }
    }
    case 'recap_updated': {
      // Recap files are global (not per-session); the counter that
      // triggers refetch lives on useSessions top-level state, NOT
      // perSession. Reducer is a no-op here.
      return { perSession: prev }
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
