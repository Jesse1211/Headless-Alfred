// Package claude wraps the in-pod `claude` CLI binary. It spawns the
// CLI with `-p --output-format stream-json`, parses each NDJSON line
// from stdout into a typed Event, and ships events on a Go channel
// suitable for forwarding to the WS handler.
//
// Reference for the on-the-wire shape: capture stream-json output
// from `claude --help` examples or run the CLI directly. Real
// fixtures live in `testdata/`.
package claude

import (
	"encoding/json"
)

// Event is one parsed item from `claude -p --output-format
// stream-json` stdout. Each line of the CLI's stdout is one JSON
// object with a top-level `type` field; we expose only the subset
// the alfred UI needs to render, plus opaque pass-through for
// anything we don't model explicitly.
//
// Discriminator-free union: callers switch on Kind. Each variant's
// fields live in their own struct, accessible via the named pointer
// (System != nil, TextDelta != nil, etc.). At most one pointer is
// non-nil per Event.
type Event struct {
	Kind EventKind

	// Variant payloads. Exactly one is non-nil per Event.
	System           *SystemEvent
	RateLimit        *RateLimitEvent
	TextDelta        *TextDeltaEvent
	TextBlockEnd     *TextBlockEndEvent
	ThinkingDelta    *ThinkingDeltaEvent
	ToolUseStart     *ToolUseStartEvent
	ToolUseEnd       *ToolUseEndEvent
	ToolResult       *ToolResultEvent
	MessageStart     *MessageStartEvent
	MessageDelta     *MessageDeltaEvent
	MessageStop      *MessageStopEvent
	Result           *ResultEvent
	TaskStarted      *TaskStartedEvent
	TaskNotification *TaskNotificationEvent
	TaskUpdated      *TaskUpdatedEvent
	HookStarted      *HookStartedEvent
	HookResponse     *HookResponseEvent
	Unknown          *UnknownEvent
}

// EventKind tags the Event union. Stable enough to use in WS frames.
type EventKind string

const (
	KindSystem        EventKind = "system"
	KindRateLimit     EventKind = "rate_limit"
	KindTextDelta     EventKind = "text_delta"
	KindTextBlockEnd  EventKind = "text_block_end"
	KindThinkingDelta EventKind = "thinking_delta"
	KindToolUseStart  EventKind = "tool_use_start"
	KindToolUseEnd    EventKind = "tool_use_end"
	KindToolResult    EventKind = "tool_result"
	KindMessageStart  EventKind = "message_start"
	KindMessageDelta  EventKind = "message_delta"
	KindMessageStop   EventKind = "message_stop"
	KindResult        EventKind = "result"
	// v0.4: CLI task + hook lifecycle. Emitted only when claude -p
	// is invoked with --include-hook-events (which buildPromptArgs
	// always does as of v0.4).
	KindTaskStarted      EventKind = "task_started"
	KindTaskNotification EventKind = "task_notification"
	KindTaskUpdated      EventKind = "task_updated"
	KindHookStarted      EventKind = "hook_started"
	KindHookResponse     EventKind = "hook_response"
	KindUnknown          EventKind = "unknown"
)

// SystemEvent — the CLI's init / status records. We pass cwd, model,
// session id back so the UI can render a small "Claude is here"
// banner.
type SystemEvent struct {
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`
	CWD       string `json:"cwd,omitempty"`
}

// RateLimitEvent — Anthropic's rate-limit telemetry. We forward it
// so the UI can surface "you're running low" warnings later (not in
// v1, but the shape is cheap to capture now).
type RateLimitEvent struct {
	Status         string `json:"status"`
	RateLimitType  string `json:"rateLimitType"`
	OverageStatus  string `json:"overageStatus"`
	ResetsAtUnix   int64  `json:"resetsAt"`
	IsUsingOverage bool   `json:"isUsingOverage"`
}

// TextDeltaEvent — one chunk of assistant text. Concatenate across
// the conversation turn to get the full response.
type TextDeltaEvent struct {
	Index int    `json:"index"` // content block index inside this message
	Text  string `json:"text"`
}

// TextBlockEndEvent — the assistant just finished a text block. UI
// can finalize Markdown rendering of the accumulated deltas.
type TextBlockEndEvent struct {
	Index int `json:"index"`
}

// ThinkingDeltaEvent — one chunk of the assistant's extended-thinking
// content (only emitted when the model has thinking enabled). Index
// identifies which content block this delta belongs to so the UI can
// accumulate parallel thinking + text blocks correctly.
type ThinkingDeltaEvent struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// ToolUseStartEvent — Claude wants to call a tool. UI shows an
// approval card. Input begins streaming as JSON deltas (see
// stream-json `input_json_delta` form); we accumulate them into
// Input in ToolUseEndEvent.
type ToolUseStartEvent struct {
	Index     int    `json:"index"`
	ToolUseID string `json:"tool_use_id"`
	Name      string `json:"name"`
}

// ToolUseEndEvent — input is complete; carry the fully-assembled
// JSON.
type ToolUseEndEvent struct {
	Index     int             `json:"index"`
	ToolUseID string          `json:"tool_use_id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// ToolResultEvent — the result of a tool execution (Bash output,
// Read result, etc.). Comes back as a `user` message with role=user
// and a tool_result content block.
type ToolResultEvent struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error"`
}

// MessageStartEvent — assistant turn beginning. Carries the initial
// usage snapshot (mostly cache stats; output_tokens is 1).
type MessageStartEvent struct {
	MessageID string `json:"id"`
	Model     string `json:"model"`
}

// MessageDeltaEvent — the assistant turn is winding down; carries
// the final usage and stop reason. Concatenating MessageDelta.Usage
// across turns gives the conversation cost.
type MessageDeltaEvent struct {
	StopReason string       `json:"stop_reason"`
	Usage      MessageUsage `json:"usage"`
}

// MessageUsage — token accounting. Field names match the CLI output.
type MessageUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// MessageStopEvent — assistant turn ended.
type MessageStopEvent struct{}

// ResultEvent — the entire `claude -p` invocation finished. Carries
// final cost (in dollars) and the assembled result text.
type ResultEvent struct {
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	DurationMs   int     `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	SessionID    string  `json:"session_id"`
}

// UnknownEvent — anything we didn't pattern-match. Preserved so the
// caller can log or forward verbatim. We never panic on an unknown
// shape — the CLI gains new event types over time, and the parser
// must not block the prompt on schema bumps.
type UnknownEvent struct {
	Type    string          `json:"type"`
	RawLine json.RawMessage `json:"-"`
}

// TaskStartedEvent — the CLI just dispatched a long-running task
// (typically Monitor's background bash process). task_id is the CLI's
// internal id; tool_use_id pairs it back to the assistant's
// tool_use block so the UI can attach this task to the right
// Monitor card. task_type observed in production: "local_bash".
type TaskStartedEvent struct {
	TaskID      string `json:"task_id"`
	ToolUseID   string `json:"tool_use_id"`
	Description string `json:"description"`
	TaskType    string `json:"task_type"`
	SessionID   string `json:"session_id,omitempty"`
}

// TaskNotificationEvent — one event from a running task's stdout
// stream. status is "in_progress" while running, "completed" on the
// terminal notification. Summary is a short human-readable label
// (e.g. `Monitor "echo ..." event`).
type TaskNotificationEvent struct {
	TaskID    string `json:"task_id"`
	ToolUseID string `json:"tool_use_id"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	SessionID string `json:"session_id,omitempty"`
}

// TaskUpdatedEvent — the GROUND-TRUTH completion signal for a task.
// Patch.Status == "completed" means the task ended. EndTime is a
// Unix millis epoch (pass through unchanged to the UI; reducer
// converts).
type TaskUpdatedEvent struct {
	TaskID    string         `json:"task_id"`
	Patch     TaskPatchField `json:"patch"`
	SessionID string         `json:"session_id,omitempty"`
}

// TaskPatchField is the inner shape of TaskUpdatedEvent.Patch.
// Kept as a separate struct so JSON tags survive round-tripping
// through the WS envelope (any -> json.Marshal preserves field
// names).
type TaskPatchField struct {
	Status  string `json:"status"`
	EndTime int64  `json:"end_time"`
}

// HookStartedEvent — a hook script is about to run. hook_event
// names the lifecycle slot ("PreToolUse" | "PostToolUse" |
// "SubagentStart" | "SubagentStop" | "Notification" | ...). hook_id
// pairs this with the matching HookResponseEvent.
type HookStartedEvent struct {
	HookID    string `json:"hook_id"`
	HookEvent string `json:"hook_event"`
	HookName  string `json:"hook_name"`
	SessionID string `json:"session_id,omitempty"`
}

// HookResponseEvent — a hook script just exited. exit_code 0 +
// outcome "success" is the happy path. The output blob is the raw
// hook stdout (passed through verbatim; reducer treats as opaque).
type HookResponseEvent struct {
	HookID    string `json:"hook_id"`
	HookEvent string `json:"hook_event"`
	ExitCode  int    `json:"exit_code"`
	Outcome   string `json:"outcome"`
	SessionID string `json:"session_id,omitempty"`
}
