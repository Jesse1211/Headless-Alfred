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
	Renderer           string `json:"renderer,omitempty"`           // "tui" | "ui" on enter_claude
	TemplateID         string `json:"templateId,omitempty"`         // enter_claude: which template to attach to the session
	BypassPermissions  *bool  `json:"bypassPermissions,omitempty"`  // enter_claude: pass --dangerously-skip-permissions to claude -p. Pointer so absent ≠ false.
	Text               string `json:"text,omitempty"`               // claude_prompt body
	// RenderTemplate, if set on a claude_prompt, replaces Text with the
	// server-side render of template.Builtins[<value>] using args the
	// server controls (recap_path from <DATA_DIR>/recaps, cwd from
	// claudeInvocationCWD, etc.). Lets the client trigger templated
	// prompts (e.g. "Generate today's recap") without owning placeholder
	// resolution. Ignored if Text is non-empty too — explicit text wins.
	RenderTemplate     string `json:"renderTemplate,omitempty"`
	ToolUseID          string `json:"toolUseId,omitempty"`          // tool_decision target
	Decision           string `json:"decision,omitempty"`           // "allow" | "deny" on tool_decision
	Reason             string `json:"reason,omitempty"`             // optional deny reason
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

	// Date carries the YYYY-MM-DD date of the recap file that was
	// written (recap_updated frame).
	Date string `json:"date,omitempty"`

	// TemplateID accompanies "idle" / "reattach" / "claude_entered"
	// when the session has an active prompt template (e.g.
	// "summary-todo"). Empty when no template is bound. Lets the
	// frontend re-mount the matching right-rail sidebar after a
	// page reload — without this, perSession.templateId would always
	// initialize to undefined on cold start.
	TemplateID string `json:"templateId,omitempty"`
}

// TypeSummaryUpdated is the WS frame Type pushed by alfred-server
// when the on-disk summary file for a session is written. The
// frame carries no body — the frontend re-fetches via
// GET /api/sessions/{sid}/summary.
const TypeSummaryUpdated = "summary_updated"

// TypeRecapUpdated is the WS frame Type pushed by alfred-server when
// a recap file (<dataDir>/recaps/<date>.md) is written. The frame
// carries the Date field so the frontend knows which recap changed
// and can re-fetch via GET /api/recaps/{date}.
const TypeRecapUpdated = "recap_updated"
