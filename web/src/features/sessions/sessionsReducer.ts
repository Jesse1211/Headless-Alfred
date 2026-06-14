import { ServerMsg } from '../../lib/ws'
import {
  PerSessionState,
  emptyPerSessionState,
  CompletedMsg,
  ClaudeState,
  ClaudeTurn,
  ClaudeToolCall,
  emptyClaudeState,
} from './types'

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
      const renderer = (m.renderer ?? cur.renderer ?? '') as PerSessionState['renderer']
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
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      const renderer = (m.renderer ?? cur.renderer ?? '') as PerSessionState['renderer']
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
    case 'claude_entered': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      next.set(m.sessionID, {
        ...cur,
        mode: 'claude',
        renderer: m.renderer ?? cur.renderer ?? 'tui',
        // Initialise claude state for UI mode if not already present.
        claude: m.renderer === 'ui' && !cur.claude ? emptyClaudeState() : cur.claude,
      })
      return { perSession: next }
    }
    case 'claude_exited': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      next.set(m.sessionID, {
        ...cur,
        mode: 'shell',
        renderer: '',
        // Keep the prior conversation history for re-display next time —
        // we just clear the "in-flight" state. (If we wanted to drop the
        // conversation on Exit we'd null `claude` here.)
        claude: cur.claude ? { ...cur.claude, inFlight: false, pending: [] } : undefined,
      })
      return { perSession: next }
    }
    case 'claude_event': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      const c = cur.claude ?? emptyClaudeState()
      const nextC = applyClaudeEvent(c, m.eventKind, m.payload)
      next.set(m.sessionID, { ...cur, claude: nextC })
      return { perSession: next }
    }
    case 'tool_approval_request': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      const c = cur.claude ?? emptyClaudeState()
      // Skip duplicates (in case the same request arrives twice).
      if (c.pending.some((p) => p.toolUseId === m.toolUseId)) {
        return { perSession: prev }
      }
      next.set(m.sessionID, {
        ...cur,
        claude: {
          ...c,
          pending: [...c.pending, { toolUseId: m.toolUseId, tool: m.tool, input: m.toolInput }],
        },
      })
      return { perSession: next }
    }
    case 'claude_error': {
      const next = new Map(prev)
      const cur = next.get(m.sessionID) ?? emptyPerSessionState()
      const c = cur.claude ?? emptyClaudeState()
      next.set(m.sessionID, {
        ...cur,
        claude: finalizeInFlightTurn(
          { ...c, lastError: { code: m.code, message: m.message } },
          m.message || m.code,
        ),
      })
      return { perSession: next }
    }
    case 'claude_run_ended': {
      // Backstop: the runner has exited. If the turn already saw a
      // `result` event, the turn is already done and finalize is a
      // no-op. If it didn't (runner crashed, was SIGINT'd before
      // result, etc.), we mark the last turn done+isError so the
      // composer unlocks and the user sees something happened.
      const cur = prev.get(m.sessionID)
      if (!cur || !cur.claude) return { perSession: prev }
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        claude: finalizeInFlightTurn(cur.claude, m.message),
      })
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

// applyClaudeEvent folds one parsed stream-json event into the
// Claude conversation state. The latest turn is mutated in place
// (immutably, via map); older turns are left untouched.
//
// We make defensive narrowing assumptions on `payload` because the
// types come over WS as `unknown`. The shapes here mirror
// internal/claude/event.go.
export function applyClaudeEvent(
  prev: ClaudeState,
  kind: string,
  payload: unknown,
): ClaudeState {
  const turns = [...prev.turns]
  const lastIdx = turns.length - 1
  const last = lastIdx >= 0 ? { ...turns[lastIdx] } : null

  switch (kind) {
    case 'system':
      // Init / status — currently a no-op for the UI. Could surface
      // model / session id later.
      return prev
    case 'message_start': {
      // A new assistant message is beginning. If we don't have a
      // turn yet (the user prompt was registered via beginClaudeTurn
      // in the hook), do nothing — wait for text deltas.
      return prev
    }
    case 'text_delta': {
      const p = payload as { text?: string } | null
      const text = p?.text ?? ''
      if (!last || last.done) return prev
      last.text = last.text + text
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'text_block_end':
      return prev
    case 'tool_use_start': {
      const p = payload as { tool_use_id?: string; name?: string } | null
      if (!p?.tool_use_id || !last || last.done) return prev
      const tools: ClaudeToolCall[] = [
        ...last.tools,
        { toolUseId: p.tool_use_id, name: p.name ?? 'tool', decision: 'pending' },
      ]
      last.tools = tools
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'tool_use_end': {
      const p = payload as { tool_use_id?: string; input?: unknown } | null
      if (!p?.tool_use_id || !last) return prev
      const tools = last.tools.map((t) =>
        t.toolUseId === p.tool_use_id ? { ...t, input: p.input } : t,
      )
      last.tools = tools
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'tool_result': {
      const p = payload as { tool_use_id?: string; content?: string; is_error?: boolean } | null
      if (!p?.tool_use_id || !last) return prev
      const tools = last.tools.map((t) =>
        t.toolUseId === p.tool_use_id
          ? { ...t, result: p.content ?? '', isError: !!p.is_error }
          : t,
      )
      last.tools = tools
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'message_delta': {
      const p = payload as { usage?: ClaudeTurn['usage'] } | null
      if (!last || !p?.usage) return prev
      last.usage = {
        inputTokens: (p.usage as any).input_tokens ?? (p.usage as any).inputTokens ?? 0,
        outputTokens: (p.usage as any).output_tokens ?? (p.usage as any).outputTokens ?? 0,
        cacheReadInputTokens:
          (p.usage as any).cache_read_input_tokens ?? (p.usage as any).cacheReadInputTokens ?? 0,
        cacheCreationInputTokens:
          (p.usage as any).cache_creation_input_tokens ??
          (p.usage as any).cacheCreationInputTokens ?? 0,
      }
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'message_stop':
      // Wait for `result` to mark the turn done — message_stop fires
      // once per assistant message but a turn may have multiple
      // messages (especially when tool use kicks in).
      return prev
    case 'result': {
      const p = payload as { is_error?: boolean; total_cost_usd?: number; result?: string } | null
      if (!last) return { ...prev, inFlight: false }
      last.done = true
      last.isError = !!p?.is_error
      last.totalCostUsd = p?.total_cost_usd
      // If the turn has no assistant text yet (auth failure, etc.),
      // surface the result string as the text so the user sees
      // something.
      if (!last.text && p?.result) {
        last.text = p.result
      }
      turns[lastIdx] = last
      return { ...prev, turns, inFlight: false }
    }
    case 'rate_limit':
    case 'unknown':
    default:
      return prev
  }
}

// beginClaudeTurn registers a fresh turn for the user's outgoing
// prompt. Called from useSessions when claude_prompt is sent.
export function beginClaudeTurn(prev: ClaudeState, prompt: string): ClaudeState {
  const turn: ClaudeTurn = {
    id: crypto.randomUUID(),
    prompt,
    startedAt: new Date().toISOString(),
    text: '',
    tools: [],
    done: false,
  }
  return { ...prev, turns: [...prev.turns, turn], inFlight: true, lastError: undefined }
}

// finalizeInFlightTurn clears inFlight and pending, and if the
// latest turn is still open (no `result` event arrived), marks it
// done + isError so the chat view stops spinning on "…" and the
// composer unlocks. Used by claude_run_ended, claude_error, and
// per-session error frames as a backstop against the runner dying
// or the backend rejecting a prompt after beginClaudeTurn already
// fired optimistically.
export function finalizeInFlightTurn(prev: ClaudeState, reason?: string): ClaudeState {
  const turns = [...prev.turns]
  const lastIdx = turns.length - 1
  if (lastIdx >= 0 && !turns[lastIdx].done) {
    const last = { ...turns[lastIdx], done: true, isError: true }
    if (!last.text && reason) {
      last.text = reason
    }
    turns[lastIdx] = last
  }
  return { ...prev, turns, inFlight: false, pending: [] }
}

// resolveClaudeTool removes a pending approval from the queue and
// optimistically marks the corresponding tool call's decision.
export function resolveClaudeTool(
  prev: ClaudeState,
  toolUseId: string,
  decision: 'allow' | 'deny',
): ClaudeState {
  const pending = prev.pending.filter((p) => p.toolUseId !== toolUseId)
  const turns = prev.turns.map((t) => ({
    ...t,
    tools: t.tools.map((tool) =>
      tool.toolUseId === toolUseId ? { ...tool, decision } : tool,
    ),
  }))
  return { ...prev, pending, turns }
}
