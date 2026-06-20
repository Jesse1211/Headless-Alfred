// ClaudeState and its sub-types mirror the Go server's
// internal/claudestate.ClaudeState exactly. The wire format
// (HTTP /claude-state, WS frames, on-disk snapshot.json) uses
// the same camelCase key names — no naming translation between
// layers. When you change a field here, change the Go side too.

export interface RunningCmd {
  id: string
  command: string
  startedAt: string
  output: string
  truncatedLossWarned: boolean
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

export type SessionMode = 'shell' | 'claude'
export type ClaudeRenderer = 'tui' | 'ui' | ''

// One turn in a Claude UI conversation. The user's prompt plus
// everything Claude produced in response (text, tool calls, tool
// results, etc.) until the next result event.
export interface ClaudeTurn {
  id: string // client-side UUID, used as React key
  // prompt is the user-visible label for the bubble — what the user
  // actually typed (or an optimistic placeholder like "Generate
  // today's recap" for template-fired prompts).
  prompt: string
  // expandedPrompt is the FULL text the server piped into `claude -p`
  // stdin — includes the rendered template body, appended summary
  // instructions, etc. Backed by the `user_prompt` WS frame the server
  // emits right after composing the final text. Same as `prompt` when
  // nothing was injected. UserPromptBubble exposes a toggle that shows
  // this when it differs from `prompt`, so the user can see exactly
  // what they paid tokens for.
  expandedPrompt?: string
  startedAt: string
  // finishedAt is when the turn ended — set by `result` (success path)
  // and by finalizeInFlightTurn (error / runner-died backstop). When
  // restored from jsonl, the backend fills this from the next user
  // row's timestamp (the closest bracket available, since assistant
  // jsonl rows carry no ts). The trailing turn in a restored history
  // may have no finishedAt; the footer hides the elapsed display then.
  finishedAt?: string
  // The assistant's reply as an ORDERED list of text + tool blocks
  // in the exact order Claude streamed them. Render each one in
  // sequence to faithfully reproduce the "Claude says something, then
  // calls a tool, then says something else, then calls another tool"
  // flow you see in the terminal. Earlier versions of this type had
  // separate `text: string` + `tools: ClaudeToolCall[]` fields, but
  // those collapsed all text into one paragraph and stacked all tools
  // at the bottom, losing the interleave.
  blocks: AssistantBlock[]
  // Extended-thinking content blocks. Independent of `blocks` (rendered
  // separately above them) because thinking is the model's internal
  // reasoning, not part of the visible reply timeline.
  thinking?: string[]
  // Private bookkeeping for live-streaming reducers: maps stream
  // content-block index → position in the corresponding array.
  // Underscore prefix flags 'transient, do not serialize'.
  _thinkingIndexMap?: Record<number, number>
  _blockIndexMap?: Record<number, number>
  // Once the result event arrives the turn is "done".
  done: boolean
  // Set by the result event.
  isError?: boolean
  /** Terminal state, source of truth. "" / undefined = in progress. */
  outcome?: 'completed' | 'errored' | 'aborted'
  /** Machine-readable interrupt reason (errored/aborted only). */
  abortReason?: string
  totalCostUsd?: number
  // Token usage (cumulative if multiple message_delta events).
  usage?: {
    inputTokens: number
    outputTokens: number
    cacheReadInputTokens: number
    cacheCreationInputTokens: number
  }
}

export interface ClaudeToolCall {
  toolUseId: string
  name: string
  // The fully-assembled input JSON Claude wants to pass to the tool.
  // Populated as text streams in via input_json_delta events; we
  // currently only show it on the approval card.
  input?: unknown
  // What the user decided when the approval card surfaced.
  // Always present on the wire: 'allow', 'deny', or 'pending'.
  decision: 'allow' | 'deny' | 'pending'
  /** Terminal state. undefined = not terminated. */
  outcome?: 'completed' | 'errored' | 'aborted' | 'denied'
  // Output from the tool, once it ran. May still be empty if denied.
  result?: string
  isError?: boolean
  // v0.4 lifecycle: timestamps for the elapsed-timer UI. startedAt is
  // recorded by the tool_use_start reducer (Date.now()); finishedAt by
  // tool_result. Undefined when the block was restored from history
  // without lifecycle events — the elapsed display short-circuits.
  startedAt?: string
  finishedAt?: string
  // CLI background-task id, set when a matching task_started event
  // arrives. Links this tool block to bgTasks[bgTaskId]. Set for
  // any tool that emits task_started (Monitor, Bash, future
  // producers).
  bgTaskId?: string
}

// BgTask tracks one Claude-CLI-spawned background task.
// Producers observed: Monitor's detached bash,
// Bash(run_in_background=true). Lifecycle is owned exclusively by
// the CLI: it spawns, monitors, and SIGKILLs in-flight tasks when
// the parent `claude -p` exits (status="killed"). alfred-server
// is the observer, not the parent.
//
// External-resource reference — not persisted in snapshot.json;
// re-derived on alfred-server restart from .jsonl replay (every
// in_progress task is forced to status="killed"). See ADR-001,
// ADR-018, and DESIGN.md.
export const BG_TASK_TOOL_NAMES = ['Monitor', 'Bash'] as const
export type BgTaskToolName = (typeof BG_TASK_TOOL_NAMES)[number]

export interface BgTask {
  taskId: string
  toolUseId: string
  description: string
  taskType: string
  startedAt: string
  finishedAt?: string
  status: 'in_progress' | 'completed' | 'failed' | 'killed' | 'stopped'
  lastEventSummary?: string
  notificationCount: number
}

// SubagentEntry tracks one in-flight subagent. Keyed by the CLI's
// hookId (SubagentStart's hook_id pairs with SubagentStop's hook_id).
// agentType is the subagent kind (e.g. "general-purpose"); we don't
// always have it on Start, so it's optional.
export interface SubagentEntry {
  hookId: string
  agentType?: string
  startedAt: string
  finishedAt?: string
}

// AssistantBlock is one item in a turn's reply timeline. The two
// kinds tag-discriminate so the renderer can dispatch on `kind`.
// Order matters — the array's order IS the rendering order.
export type AssistantBlock =
  | { kind: 'text'; text: string }
  | { kind: 'tool'; tool: ClaudeToolCall }

// Helper for legacy code paths that want the full assistant text as
// one string (e.g. for measuring token usage, copy-to-clipboard).
// New code should iterate blocks directly.
export function joinAssistantText(blocks: AssistantBlock[]): string {
  return blocks
    .filter((b): b is { kind: 'text'; text: string } => b.kind === 'text')
    .map((b) => b.text)
    .join('')
}

export interface ClaudeState {
  // The conversation as a list of turns, oldest first.
  turns: ClaudeTurn[]
  // True once useClaudeHistoryLoader has done its one-shot fetch
  // from the backend jsonl-restore endpoint. Cleared on claude_exited
  // so re-entering re-runs the fetch (the underlying uuid may have
  // rotated). Cleared on WS reconnect for whichever session is
  // currently selected (see useSessions wsEpoch handling) so the
  // user never looks at stale state.
  turnsLoaded?: boolean
  // The wsEpoch this state was hydrated at. Lets useSessions notice
  // that a background session loaded before the most recent reconnect
  // is stale and trigger a refetch when the user finally switches to it.
  hydrateEpoch?: number
  // True while a claude_prompt is in flight (claude -p running).
  inFlight: boolean
  // Pending tool approvals waiting for the user.
  pending: ClaudeToolApprovalRequest[]
  // Pending AskUserQuestion invocations — each one renders a dedicated
  // question card (radio / multi-select / "Other") instead of a
  // generic Allow/Deny. User's answer rides back to Claude via the
  // hook's deny+reason channel, which CLI surfaces as the tool's
  // tool_result.
  pendingQuestions: ClaudeQuestionRequest[]
  // Last error returned by the backend (e.g., claude not authenticated).
  lastError?: { code: string; message: string }
  // v0.4: ground-truth lifecycle tracking. Keyed by taskId / hookId
  // respectively. Both are session-scoped; cleared on claude_exited.
  bgTasks: Record<string, BgTask>
  subagents: Record<string, SubagentEntry>
  // Live stdout tail for background tasks. Keyed by taskId; value is
  // decoded text (UTF-8 via atob). Capped at 64KB tail: bytes over
  // the cap are head-trimmed so the most-recent output is always
  // preserved. Populated by bg_task_stdout_chunk WS frames.
  bgTaskLogs: Record<string, string>
}

export interface ClaudeToolApprovalRequest {
  toolUseId: string
  tool: string
  input: unknown
}

// Mirrors the AskUserQuestion tool input shape emitted by Claude
// (see claude --help / CLI source). Each tool invocation can carry
// multiple questions; users answer all of them in one card.
export interface ClaudeQuestionRequest {
  toolUseId: string
  questions: ClaudeQuestion[]
}

export interface ClaudeQuestion {
  question: string
  header: string
  multiSelect: boolean
  options: ClaudeQuestionOption[]
}

export interface ClaudeQuestionOption {
  label: string
  description?: string
}

export interface PerSessionState {
  running: RunningCmd | null
  messages: CompletedMsg[]
  messagesLoaded: boolean
  mode: SessionMode
  renderer?: ClaudeRenderer
  // Template id active for this session's Claude run (e.g. 'summary-todo').
  // Set by useSessions.enterClaude when the user opts in via
  // StartClaudeDialog; cleared on claude_exited.
  templateId?: string
  // Bumped on every WS summary_updated frame for this session. The
  // summary sidebar's fetch effect depends on this counter, so any
  // bump triggers a re-fetch of the summary file content.
  summaryFetchCounter?: number
  // Bumped on every WS note_updated frame for this session. Mirrors
  // summaryFetchCounter; the NotesPanel's read effect depends on it
  // so any push triggers a re-fetch.
  noteFetchCounter?: number
  claude?: ClaudeState
}

export function emptyPerSessionState(): PerSessionState {
  return { running: null, messages: [], messagesLoaded: false, mode: 'shell', renderer: '' }
}

export function emptyClaudeState(): ClaudeState {
  return { turns: [], inFlight: false, pending: [], pendingQuestions: [], bgTasks: {}, subagents: {}, bgTaskLogs: {} }
}
