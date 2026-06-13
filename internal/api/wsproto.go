package api

// Inbound and Outbound WebSocket message shapes. Exported because the
// e2e test suite reuses them (no point hand-maintaining two copies).

// InMsg is a client → server WS frame.
//
// Types added incrementally across rollouts:
//   - V0 chat:           "run"
//   - V0 claude (TUI):   "enter_claude", "exit_claude", "stdin"
//   - V1 claude (UI):    "claude_prompt", "tool_decision", "interrupt"
//   - Renderer field on "enter_claude"
type InMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID,omitempty"`
	Command   string `json:"command,omitempty"`
	Data      string `json:"data,omitempty"` // base64-encoded raw stdin bytes (stdin frame)

	// V1 claude UI renderer additions:
	Renderer  string `json:"renderer,omitempty"`  // "tui" | "ui" on enter_claude
	Text      string `json:"text,omitempty"`      // claude_prompt body
	ToolUseID string `json:"toolUseId,omitempty"` // tool_decision target
	Decision  string `json:"decision,omitempty"`  // "allow" | "deny" on tool_decision
	Reason    string `json:"reason,omitempty"`    // optional deny reason
}

// OutMsg is a server → client WS frame.
//
// Types added incrementally across rollouts:
//   - V0 chat:           "idle", "reattach", "started", "chunk", "done",
//     "error", "session_closed", "session_renamed",
//     "session_created"
//   - V0 claude (TUI):   "claude_entered", "claude_exited", "pty_data"
//   - Mode field on idle/reattach
//   - V1 claude (UI):    "claude_event", "tool_approval_request",
//     "claude_error"
//   - Renderer field on idle/reattach/claude_entered
type OutMsg struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionID,omitempty"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Name        string `json:"name,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	Mode        string `json:"mode,omitempty"` // "shell" | "claude" (on idle/reattach)

	// V1 claude UI renderer additions:

	// Renderer accompanies "idle" / "reattach" / "claude_entered"
	// when Mode == "claude" so the frontend can mount the correct
	// view (ClaudeChatView for "ui", ClaudeTerminal for "tui"). Empty
	// for non-claude sessions.
	Renderer string `json:"renderer,omitempty"`

	// EventKind classifies a "claude_event" payload (text_delta,
	// tool_use_start, message_delta, result, etc.). See
	// internal/claude.EventKind for the source-of-truth list.
	EventKind string `json:"eventKind,omitempty"`

	// Payload carries the marshalled fields of one parsed
	// internal/claude.Event variant — the kind is in EventKind. JSON
	// type is intentionally any so we can ship variant-specific
	// shapes (TextDeltaEvent, ResultEvent, ToolUseStartEvent, ...)
	// without enumerating fields here.
	Payload any `json:"payload,omitempty"`

	// ToolUseID identifies the pending tool-approval the client is
	// looking at (tool_approval_request frame).
	ToolUseID string `json:"toolUseId,omitempty"`

	// Tool / ToolInput describe what the user is being asked to
	// approve (tool_approval_request frame).
	Tool      string `json:"tool,omitempty"`
	ToolInput any    `json:"toolInput,omitempty"`
}
