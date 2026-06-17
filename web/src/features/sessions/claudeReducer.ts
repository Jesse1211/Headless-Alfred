// Claude-UI–specific reducer slice. The shell-lifecycle cases
// (started/chunk/done/idle/reattach) stay in sessionsReducer.ts; this
// file owns everything that touches `claude: ClaudeState` —
// claude_entered, claude_exited, claude_event, tool_approval_request,
// claude_error, claude_run_ended — plus the small helpers that
// useSessions calls to drive optimistic UI (beginClaudeTurn,
// resolveClaudeTool, finalizeInFlightTurn).
//
// applyClaudeEvent is the inner state machine that folds one parsed
// stream-json event into a ClaudeState; everything else delegates to
// it.
import { ServerMsg } from '../../lib/ws'
import { randomId } from '../../lib/randomId'
import {
  PerSessionState,
  emptyPerSessionState,
  ClaudeState,
  ClaudeTurn,
  ClaudeToolCall,
  ClaudeQuestion,
  AssistantBlock,
  emptyClaudeState,
} from './types'

// mutateClaude returns a new perSession map with `mut` applied to the
// target session's claude state. If the session has no claude state
// yet, it is initialised to emptyClaudeState() first. Centralises the
// `next = new Map(prev); cur.claude ?? emptyClaudeState(); next.set(…)`
// boilerplate every Claude case used to repeat.
function mutateClaude(
  prev: Map<string, PerSessionState>,
  sessionID: string,
  mut: (c: ClaudeState) => ClaudeState,
): Map<string, PerSessionState> {
  const cur = prev.get(sessionID) ?? emptyPerSessionState()
  const c = cur.claude ?? emptyClaudeState()
  const next = new Map(prev)
  next.set(sessionID, { ...cur, claude: mut(c) })
  return next
}

// reduceClaudeMsg handles every WS frame that affects Claude state.
// Returns the next perSession map, or `null` if the message isn't a
// Claude frame (so the caller can fall through to the shell reducer).
export function reduceClaudeMsg(
  prev: Map<string, PerSessionState>,
  m: ServerMsg,
): Map<string, PerSessionState> | null {
  switch (m.type) {
    case 'claude_entered': {
      const cur = prev.get(m.sessionID) ?? emptyPerSessionState()
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        mode: 'claude',
        renderer: m.renderer ?? cur.renderer ?? 'tui',
        // Initialise claude state for UI mode if not already present.
        claude: m.renderer === 'ui' && !cur.claude ? emptyClaudeState() : cur.claude,
      })
      return next
    }
    case 'claude_exited': {
      const cur = prev.get(m.sessionID) ?? emptyPerSessionState()
      const next = new Map(prev)
      next.set(m.sessionID, {
        ...cur,
        mode: 'shell',
        renderer: '',
        templateId: undefined,
        // Keep the prior conversation history for re-display next time —
        // we just clear the "in-flight" state. (If we wanted to drop
        // the conversation on Exit we'd null `claude` here.) turnsLoaded
        // is reset so the next enter triggers a fresh history fetch — the
        // underlying ClaudeSessionID may have rotated.
        claude: cur.claude
          ? {
              ...cur.claude,
              inFlight: false,
              pending: [],
              pendingQuestions: [],
              turnsLoaded: false,
              bgTasks: {},
              subagents: {},
            }
          : undefined,
      })
      return next
    }
    case 'claude_event':
      return mutateClaude(prev, m.sessionID, (c) => applyClaudeEvent(c, m.eventKind, m.payload))
    case 'tool_approval_request': {
      const c = prev.get(m.sessionID)?.claude ?? emptyClaudeState()
      // Dedup against BOTH queues — a re-emitted toolUseId could
      // belong to either depending on which branch ran first.
      const alreadyQueued =
        c.pending.some((p) => p.toolUseId === m.toolUseId) ||
        c.pendingQuestions.some((q) => q.toolUseId === m.toolUseId)
      if (alreadyQueued) return prev
      // AskUserQuestion is special: it IS a question for the user, not
      // "may I run this?". Route well-formed input to the dedicated
      // question card; the answer rides back through
      // tool_decision('deny', reason) so the CLI surfaces it as the
      // tool's tool_result. Malformed input falls through to the
      // generic approval card so the user can at least see and
      // dismiss the call instead of the runner hanging.
      if (m.tool === 'AskUserQuestion') {
        const questions = parseAskUserQuestionInput(m.toolInput)
        if (questions.length > 0) {
          return mutateClaude(prev, m.sessionID, (cc) => ({
            ...cc,
            pendingQuestions: [...cc.pendingQuestions, { toolUseId: m.toolUseId, questions }],
          }))
        }
      }
      return mutateClaude(prev, m.sessionID, (cc) => ({
        ...cc,
        pending: [...cc.pending, { toolUseId: m.toolUseId, tool: m.tool, input: m.toolInput }],
      }))
    }
    case 'claude_error':
      return mutateClaude(prev, m.sessionID, (c) =>
        finalizeInFlightTurn(
          { ...c, lastError: { code: m.code, message: m.message } },
          m.message || m.code,
        ),
      )
    case 'claude_run_ended': {
      // Backstop: the runner has exited. If the turn already saw a
      // `result` event, the turn is already done and finalize is a
      // no-op. If it didn't (runner crashed, was SIGINT'd before
      // result, etc.), we mark the last turn done+isError so the
      // composer unlocks and the user sees something happened.
      const cur = prev.get(m.sessionID)
      if (!cur || !cur.claude) return prev
      const next = new Map(prev)
      next.set(m.sessionID, { ...cur, claude: finalizeInFlightTurn(cur.claude, m.message) })
      return next
    }
    case 'user_prompt':
      // Server emits this right after composing the final prompt body
      // (template-injected, summary-suffixed, etc.). Stash on the
      // most-recent turn so UserPromptBubble can offer a "show full
      // prompt" toggle.
      return mutateClaude(prev, m.sessionID, (c) => attachExpandedPromptToLastTurn(c, m.text))
    default:
      return null
  }
}

// attachExpandedPromptToLastTurn writes the fully composed prompt
// body onto the most recent turn. Called from the user_prompt frame
// handler. If there is no current turn (shouldn't happen — the
// optimistic beginClaudeTurn always runs first), this is a no-op.
function attachExpandedPromptToLastTurn(prev: ClaudeState, text: string): ClaudeState {
  const turns = [...prev.turns]
  const lastIdx = turns.length - 1
  if (lastIdx < 0) return prev
  turns[lastIdx] = { ...turns[lastIdx], expandedPrompt: text }
  return { ...prev, turns }
}

// applyClaudeEvent folds one parsed stream-json event into the
// Claude conversation state. The latest turn is mutated in place
// (immutably, via map); older turns are left untouched.
//
// `payload` arrives as `unknown` over the WS, so we narrow with
// per-kind type guards (see asXxx helpers below). The shapes mirror
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
    case 'message_start':
    case 'text_block_end':
    case 'message_stop':
    case 'rate_limit':
    case 'unknown':
      // Currently no-op for the UI. Could surface model / session id
      // / rate-limit warnings later. message_stop fires per assistant
      // message but a turn may have several when tool use kicks in,
      // so we wait for `result` to mark the turn done.
      return prev
    case 'text_delta': {
      const p = asTextDelta(payload)
      if (!last || last.done) return prev
      // Stream deltas land in the block at content-block index p.index.
      // We track index → array position on the turn; if that index has
      // no block yet, push a fresh text block. text_delta arriving at
      // an index that's currently holding a tool block (shouldn't
      // happen in well-formed streams, but defensive) opens a new text
      // block at that index — last write wins.
      const blocks = last.blocks ? last.blocks.slice() : []
      const positions = last._blockIndexMap ? { ...last._blockIndexMap } : {}
      let pos = positions[p.index]
      if (pos === undefined || blocks[pos]?.kind !== 'text') {
        pos = blocks.length
        positions[p.index] = pos
        blocks.push({ kind: 'text', text: '' })
      }
      const existing = blocks[pos] as { kind: 'text'; text: string }
      blocks[pos] = { kind: 'text', text: existing.text + p.text }
      last.blocks = blocks
      last._blockIndexMap = positions
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'thinking_delta': {
      const p = asThinkingDelta(payload)
      if (!last || last.done) return prev
      // Same per-index accumulation, but into a SEPARATE thinking[]
      // array (rendered above the visible blocks timeline) since
      // thinking isn't part of the user-facing reply.
      const tblocks = last.thinking ? last.thinking.slice() : []
      const positions = last._thinkingIndexMap ? { ...last._thinkingIndexMap } : {}
      let pos = positions[p.index]
      if (pos === undefined) {
        pos = tblocks.length
        positions[p.index] = pos
        tblocks.push('')
      }
      tblocks[pos] = tblocks[pos] + p.text
      last.thinking = tblocks
      last._thinkingIndexMap = positions
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'tool_use_start': {
      const p = asToolUseStart(payload)
      if (!p.toolUseId || !last || last.done) return prev
      // Push a new tool block at the next slot and remember its index
      // so subsequent tool_use_end / tool_result events can patch the
      // right block via the toolUseId match.
      const blocks = last.blocks ? last.blocks.slice() : []
      const positions = last._blockIndexMap ? { ...last._blockIndexMap } : {}
      const pos = blocks.length
      positions[p.index] = pos
      blocks.push({
        kind: 'tool',
        tool: {
          toolUseId: p.toolUseId,
          name: p.name,
          decision: 'pending',
          startedAt: new Date().toISOString(),
        },
      })
      last.blocks = blocks
      last._blockIndexMap = positions
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'tool_use_end': {
      const p = asToolUseEnd(payload)
      if (!p.toolUseId || !last) return prev
      last.blocks = patchToolBlock(last.blocks, p.toolUseId, (t) => ({ ...t, input: p.input }))
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'tool_result': {
      const p = asToolResult(payload)
      if (!p.toolUseId || !last) return prev
      last.blocks = patchToolBlock(last.blocks, p.toolUseId, (t) => ({
        ...t,
        result: p.content,
        isError: p.isError,
        finishedAt: new Date().toISOString(),
      }))
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'message_delta': {
      const usage = asMessageDeltaUsage(payload)
      if (!last || !usage) return prev
      last.usage = usage
      turns[lastIdx] = last
      return { ...prev, turns }
    }
    case 'result': {
      const p = asResult(payload)
      if (!last) return { ...prev, inFlight: false }
      last.done = true
      last.isError = p.isError
      last.totalCostUsd = p.totalCostUsd
      // If the turn has no assistant text yet (auth failure, etc.),
      // surface the result string as a synthetic final text block so
      // the user sees something.
      if (last.blocks.length === 0 && p.result) {
        last.blocks = [{ kind: 'text', text: p.result }]
      }
      turns[lastIdx] = last
      return { ...prev, turns, inFlight: false }
    }
    case 'task_started': {
      const p = asTaskStarted(payload)
      if (!p.taskId) return prev
      const bgTasks = {
        ...prev.bgTasks,
        [p.taskId]: {
          taskId: p.taskId,
          toolUseId: p.toolUseId,
          description: p.description,
          taskType: p.taskType,
          startedAt: new Date().toISOString(),
          status: 'in_progress' as const,
          notificationCount: 0,
        },
      }
      // If a tool block exists with this tool_use_id, link it via
      // bgTaskId so the Monitor card can render the task-aware UI.
      const linkedTurns = prev.turns.map((t) => ({
        ...t,
        blocks: patchToolBlock(t.blocks, p.toolUseId, (tool) => ({ ...tool, bgTaskId: p.taskId })),
      }))
      return { ...prev, bgTasks, turns: linkedTurns }
    }
    case 'task_notification': {
      const p = asTaskNotification(payload)
      if (!p.taskId || !prev.bgTasks[p.taskId]) return prev
      const cur = prev.bgTasks[p.taskId]
      const bgTasks = {
        ...prev.bgTasks,
        [p.taskId]: {
          ...cur,
          notificationCount: cur.notificationCount + 1,
          lastEventSummary: p.summary,
          // Some CLIs emit the final 'completed' status on task_notification
          // before/instead of task_updated. Mirror it through so the UI
          // freezes regardless of which arrives first.
          status: p.status === 'completed' ? 'completed' as const : cur.status,
          finishedAt: p.status === 'completed' && !cur.finishedAt
            ? new Date().toISOString()
            : cur.finishedAt,
        },
      }
      return { ...prev, bgTasks }
    }
    case 'task_updated': {
      const p = asTaskUpdated(payload)
      if (!p.taskId || !prev.bgTasks[p.taskId]) return prev
      const cur = prev.bgTasks[p.taskId]
      if (p.status !== 'completed' && p.status !== 'failed') return prev
      const bgTasks = {
        ...prev.bgTasks,
        [p.taskId]: {
          ...cur,
          status: p.status,
          finishedAt: p.endTime
            ? new Date(p.endTime).toISOString()
            : new Date().toISOString(),
        },
      }
      return { ...prev, bgTasks }
    }
    case 'hook_started': {
      const p = asHookStarted(payload)
      if (p.hookEvent !== 'SubagentStart' || !p.hookId) return prev
      const subagents = {
        ...prev.subagents,
        [p.hookId]: {
          hookId: p.hookId,
          startedAt: new Date().toISOString(),
        },
      }
      return { ...prev, subagents }
    }
    case 'hook_response': {
      const p = asHookResponse(payload)
      if (p.hookEvent !== 'SubagentStop') return prev
      // SubagentStop hook_id != SubagentStart hook_id; we mark the
      // OLDEST in-progress subagent as finished (FIFO pairing).
      const entries = Object.entries(prev.subagents).filter(
        ([, e]) => !e.finishedAt,
      )
      if (entries.length === 0) return prev
      // Sort by startedAt ASC; pick the first.
      entries.sort(([, a], [, b]) => a.startedAt.localeCompare(b.startedAt))
      const [oldestId, oldest] = entries[0]
      const subagents = {
        ...prev.subagents,
        [oldestId]: { ...oldest, finishedAt: new Date().toISOString() },
      }
      return { ...prev, subagents }
    }
    default:
      return prev
  }
}

// patchToolBlock returns a new blocks array with the tool block
// whose toolUseId matches `id` replaced by patch(prev). Non-matching
// blocks and text blocks are untouched. Returns the input array
// unchanged if no match (no allocation).
function patchToolBlock(
  blocks: AssistantBlock[],
  id: string,
  patch: (t: ClaudeToolCall) => ClaudeToolCall,
): AssistantBlock[] {
  let changed = false
  const next = blocks.map((b) => {
    if (b.kind === 'tool' && b.tool.toolUseId === id) {
      changed = true
      return { kind: 'tool' as const, tool: patch(b.tool) }
    }
    return b
  })
  return changed ? next : blocks
}

// beginClaudeTurn registers a fresh turn for the user's outgoing
// prompt. Called from useSessions when claude_prompt is sent.
export function beginClaudeTurn(prev: ClaudeState, prompt: string): ClaudeState {
  const turn: ClaudeTurn = {
    id: randomId(),
    prompt,
    startedAt: new Date().toISOString(),
    blocks: [],
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
    // If the turn produced nothing visible yet (auth failure / runner
    // died / 5xx before any block streamed), surface `reason` as a
    // synthetic text block so the user sees something other than a
    // silent empty bubble.
    if (last.blocks.length === 0 && reason) {
      last.blocks = [{ kind: 'text', text: reason }]
    }
    turns[lastIdx] = last
  }
  return { ...prev, turns, inFlight: false, pending: [], pendingQuestions: [] }
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
    blocks: patchToolBlock(t.blocks, toolUseId, (tool) => ({ ...tool, decision })),
  }))
  return { ...prev, pending, turns }
}

// resolveClaudeQuestion drops the answered question from the queue.
// The actual answer text rides back through tool_decision('deny',
// reason); the CLI converts the deny reason into the tool's
// tool_result so Claude sees the user's choice on the next turn.
export function resolveClaudeQuestion(
  prev: ClaudeState,
  toolUseId: string,
): ClaudeState {
  const pendingQuestions = prev.pendingQuestions.filter((q) => q.toolUseId !== toolUseId)
  return { ...prev, pendingQuestions }
}

// parseAskUserQuestionInput narrows the AskUserQuestion tool input
// JSON into a typed list. Returns [] if the shape doesn't match (so
// the reducer can fall back to the generic approval card).
export function parseAskUserQuestionInput(input: unknown): ClaudeQuestion[] {
  const i = input as { questions?: unknown } | null
  if (!i || !Array.isArray(i.questions)) return []
  const out: ClaudeQuestion[] = []
  for (const q of i.questions) {
    const qq = q as {
      question?: unknown
      header?: unknown
      multiSelect?: unknown
      options?: unknown
    } | null
    if (!qq || typeof qq.question !== 'string') continue
    const opts = Array.isArray(qq.options) ? qq.options : []
    const options = opts
      .map((o) => o as { label?: unknown; description?: unknown } | null)
      .filter((o): o is { label: string; description?: string } => !!o && typeof o.label === 'string')
      .map((o) => ({ label: o.label, description: typeof o.description === 'string' ? o.description : undefined }))
    out.push({
      question: qq.question,
      header: typeof qq.header === 'string' ? qq.header : '',
      multiSelect: !!qq.multiSelect,
      options,
    })
  }
  return out
}

// ---- payload narrowing ---------------------------------------------------

function asTextDelta(payload: unknown): { index: number; text: string } {
  const p = payload as { index?: number; text?: string } | null
  return { index: p?.index ?? 0, text: p?.text ?? '' }
}

function asThinkingDelta(payload: unknown): { index: number; text: string } {
  const p = payload as { index?: number; text?: string } | null
  return { index: p?.index ?? 0, text: p?.text ?? '' }
}

function asToolUseStart(payload: unknown): { index: number; toolUseId: string; name: string } {
  const p = payload as { index?: number; tool_use_id?: string; name?: string } | null
  return { index: p?.index ?? 0, toolUseId: p?.tool_use_id ?? '', name: p?.name ?? 'tool' }
}

function asToolUseEnd(payload: unknown): { toolUseId: string; input: unknown } {
  const p = payload as { tool_use_id?: string; input?: unknown } | null
  return { toolUseId: p?.tool_use_id ?? '', input: p?.input }
}

function asToolResult(
  payload: unknown,
): { toolUseId: string; content: string; isError: boolean } {
  const p = payload as { tool_use_id?: string; content?: string; is_error?: boolean } | null
  return {
    toolUseId: p?.tool_use_id ?? '',
    content: p?.content ?? '',
    isError: !!p?.is_error,
  }
}

function asMessageDeltaUsage(payload: unknown): ClaudeTurn['usage'] | null {
  const p = payload as { usage?: Record<string, number> } | null
  const u = p?.usage
  if (!u) return null
  // The CLI uses snake_case; older code also tolerated camelCase. Keep
  // both for forward-compat with any wrapper that pre-normalises.
  return {
    inputTokens: u.input_tokens ?? u.inputTokens ?? 0,
    outputTokens: u.output_tokens ?? u.outputTokens ?? 0,
    cacheReadInputTokens: u.cache_read_input_tokens ?? u.cacheReadInputTokens ?? 0,
    cacheCreationInputTokens:
      u.cache_creation_input_tokens ?? u.cacheCreationInputTokens ?? 0,
  }
}

function asResult(
  payload: unknown,
): { isError: boolean; totalCostUsd?: number; result?: string } {
  const p = payload as { is_error?: boolean; total_cost_usd?: number; result?: string } | null
  return {
    isError: !!p?.is_error,
    totalCostUsd: p?.total_cost_usd,
    result: p?.result,
  }
}

function asTaskStarted(
  payload: unknown,
): { taskId: string; toolUseId: string; description: string; taskType: string } {
  const p = payload as {
    task_id?: string
    tool_use_id?: string
    description?: string
    task_type?: string
  } | null
  return {
    taskId: p?.task_id ?? '',
    toolUseId: p?.tool_use_id ?? '',
    description: p?.description ?? '',
    taskType: p?.task_type ?? '',
  }
}

function asTaskNotification(
  payload: unknown,
): { taskId: string; toolUseId: string; status: string; summary: string } {
  const p = payload as {
    task_id?: string
    tool_use_id?: string
    status?: string
    summary?: string
  } | null
  return {
    taskId: p?.task_id ?? '',
    toolUseId: p?.tool_use_id ?? '',
    status: p?.status ?? '',
    summary: p?.summary ?? '',
  }
}

function asTaskUpdated(
  payload: unknown,
): { taskId: string; status: string; endTime: number } {
  const p = payload as {
    task_id?: string
    patch?: { status?: string; end_time?: number }
  } | null
  return {
    taskId: p?.task_id ?? '',
    status: p?.patch?.status ?? '',
    endTime: p?.patch?.end_time ?? 0,
  }
}

function asHookStarted(
  payload: unknown,
): { hookId: string; hookEvent: string; hookName: string } {
  const p = payload as {
    hook_id?: string
    hook_event?: string
    hook_name?: string
  } | null
  return {
    hookId: p?.hook_id ?? '',
    hookEvent: p?.hook_event ?? '',
    hookName: p?.hook_name ?? '',
  }
}

function asHookResponse(
  payload: unknown,
): { hookId: string; hookEvent: string; exitCode: number; outcome: string } {
  const p = payload as {
    hook_id?: string
    hook_event?: string
    exit_code?: number
    outcome?: string
  } | null
  return {
    hookId: p?.hook_id ?? '',
    hookEvent: p?.hook_event ?? '',
    exitCode: p?.exit_code ?? 0,
    outcome: p?.outcome ?? '',
  }
}
