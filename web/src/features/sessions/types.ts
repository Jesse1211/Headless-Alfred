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
  prompt: string
  startedAt: string
  // Accumulated assistant text deltas in order.
  text: string
  // Tool calls Claude requested during this turn, in order.
  tools: ClaudeToolCall[]
  // Once the result event arrives the turn is "done".
  done: boolean
  // Set by the result event.
  isError?: boolean
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
  decision?: 'allow' | 'deny' | 'pending'
  // Output from the tool, once it ran. May still be empty if denied.
  result?: string
  isError?: boolean
}

export interface ClaudeState {
  // The conversation as a list of turns, oldest first.
  turns: ClaudeTurn[]
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
  claude?: ClaudeState
}

export function emptyPerSessionState(): PerSessionState {
  return { running: null, messages: [], messagesLoaded: false, mode: 'shell', renderer: '' }
}

export function emptyClaudeState(): ClaudeState {
  return { turns: [], inFlight: false, pending: [], pendingQuestions: [] }
}
