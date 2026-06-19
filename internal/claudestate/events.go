package claudestate

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventKind tags one variant of the Event union. Values are kept
// identical to the WS frame `eventKind` field the frontend already
// uses, so a single string drives both the Go reducer dispatch and
// the TS reducer dispatch.
type EventKind string

const (
	EventMessageStart  EventKind = "message_start"
	EventMessageDelta  EventKind = "message_delta"
	EventMessageStop   EventKind = "message_stop"
	EventTextDelta     EventKind = "text_delta"
	EventThinkingDelta EventKind = "thinking_delta"
	EventTextBlockEnd  EventKind = "text_block_end"
	EventToolUseStart  EventKind = "tool_use_start"
	EventToolUseEnd    EventKind = "tool_use_end"
	EventToolResult    EventKind = "tool_result"
	EventResult        EventKind = "result"

	// User-driven events arriving from the client.
	EventToolDecision EventKind = "tool_decision"

	// Hook-driven events.
	EventTaskStarted      EventKind = "task_started"
	EventTaskNotification EventKind = "task_notification"
	EventTaskUpdated      EventKind = "task_updated"
	EventHookStarted      EventKind = "hook_started"
	EventHookResponse     EventKind = "hook_response"

	// Lifecycle events the server itself emits.
	EventClaudeError    EventKind = "claude_error"
	EventClaudeRunEnded EventKind = "claude_run_ended"

	// Optimistic-UI reconciliation events broadcast after Apply.
	EventTurnStarted         EventKind = "turn_started"
	EventToolDecisionApplied EventKind = "tool_decision_applied"

	// Catch-all for stream-json kinds we don't care about.
	EventUnknown EventKind = "unknown"

	// Stream-json metadata kinds the claudestate reducer ignores —
	// declared so Event.UnmarshalJSON can route them to the no-payload
	// branch instead of erroring out and producing log spam at the
	// WS dispatch boundary. (system carries model/session metadata,
	// rate_limit advisory info; neither needs to mutate ClaudeState.)
	EventSystem    EventKind = "system"
	EventRateLimit EventKind = "rate_limit"
)

// Event is the input to SessionState.Apply. Timestamp is the
// server's Apply-time wall clock; reducers stamp this into state
// fields like Turn.FinishedAt or ToolCall.StartedAt verbatim. The
// same Event is broadcast to clients so their reducer reaches the
// same state.
type Event struct {
	Kind      EventKind `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload"`
}

// eventWire is the on-wire shape used for unmarshal. Payload arrives
// as raw JSON and is decoded against the concrete type chosen by
// Kind. This lets one channel carry every event variant without an
// interface{}-vs-struct gymnastics dance.
type eventWire struct {
	Kind      EventKind       `json:"kind"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// UnmarshalJSON decodes Payload into the concrete struct matching Kind.
// Unknown kinds keep the raw RawMessage in Payload so the caller can
// inspect it without losing data.
func (e *Event) UnmarshalJSON(data []byte) error {
	var w eventWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	e.Kind = w.Kind
	e.Timestamp = w.Timestamp
	var pl any
	switch w.Kind {
	case EventTextDelta:
		pl = &TextDeltaPayload{}
	case EventThinkingDelta:
		pl = &ThinkingDeltaPayload{}
	case EventToolUseStart:
		pl = &ToolUseStartPayload{}
	case EventToolUseEnd:
		pl = &ToolUseEndPayload{}
	case EventToolResult:
		pl = &ToolResultPayload{}
	case EventMessageDelta:
		pl = &MessageDeltaPayload{}
	case EventResult:
		pl = &ResultPayload{}
	case EventTaskStarted:
		pl = &TaskStartedPayload{}
	case EventTaskNotification:
		pl = &TaskNotificationPayload{}
	case EventTaskUpdated:
		pl = &TaskUpdatedPayload{}
	case EventHookStarted:
		pl = &HookStartedPayload{}
	case EventHookResponse:
		pl = &HookResponsePayload{}
	case EventToolDecision:
		pl = &ToolDecisionPayload{}
	case EventTurnStarted:
		pl = &TurnStartedPayload{}
	case EventToolDecisionApplied:
		pl = &ToolDecisionAppliedPayload{}
	case EventClaudeError:
		pl = &ClaudeErrorPayload{}
	case EventClaudeRunEnded:
		pl = &ClaudeRunEndedPayload{}
	case EventMessageStart, EventMessageStop, EventTextBlockEnd, EventUnknown,
		EventSystem, EventRateLimit:
		// No payload of interest — reducer ignores these (metadata /
		// advisory frames). Declared explicitly so dispatch doesn't
		// hit the default error branch and produce WARN log spam.
		e.Payload = nil
		return nil
	default:
		return fmt.Errorf("claudestate: unknown event kind %q", w.Kind)
	}
	if len(w.Payload) > 0 {
		if err := json.Unmarshal(w.Payload, pl); err != nil {
			return fmt.Errorf("claudestate: payload decode (kind=%s): %w", w.Kind, err)
		}
	}
	e.Payload = pl
	return nil
}

// MarshalJSON wraps Payload in the eventWire shape so encoding stays
// symmetric with UnmarshalJSON.
func (e Event) MarshalJSON() ([]byte, error) {
	pl, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(eventWire{
		Kind: e.Kind, Timestamp: e.Timestamp, Payload: pl,
	})
}

// ---- payload struct definitions -----------------------------------
//
// The first eleven payload types decode JSON produced by
// internal/claude (Claude CLI stream-json + hook events). The wire
// format there is snake_case (input_tokens, is_error, tool_use_id, ...)
// — these structs must match field-for-field so the dispatch path's
// marshal→unmarshal round-trip preserves data. The frontend reducer's
// narrowing helpers (asXxx in claudeReducer.ts) already accept
// snake_case wire payloads, so this is consistent end-to-end.
//
// The remaining four payload types (ToolDecision, TurnStarted,
// ToolDecisionApplied, ClaudeError, ClaudeRunEnded) are server-
// originated frames; the frontend reducer consumes them as camelCase
// via OutMsg fields and the WS protocol mirror in lib/ws.ts.

type TextDeltaPayload struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type ThinkingDeltaPayload struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type ToolUseStartPayload struct {
	Index     int    `json:"index"`
	ToolUseID string `json:"tool_use_id"`
	Name      string `json:"name"`
}

type ToolUseEndPayload struct {
	ToolUseID string `json:"tool_use_id"`
	Input     any    `json:"input,omitempty"`
}

type ToolResultPayload struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error"`
}

// MessageDeltaUsage mirrors internal/claude.MessageUsage's wire shape
// (snake_case). The reducer copies into the camelCase TokenUsage that
// lives on ClaudeTurn.
type MessageDeltaUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type MessageDeltaPayload struct {
	Usage MessageDeltaUsage `json:"usage"`
}

type ResultPayload struct {
	IsError      bool    `json:"is_error"`
	TotalCostUsd float64 `json:"total_cost_usd"`
	Result       string  `json:"result,omitempty"`
}

type TaskStartedPayload struct {
	TaskID      string `json:"task_id"`
	ToolUseID   string `json:"tool_use_id"`
	Description string `json:"description"`
	TaskType    string `json:"task_type"`
}

type TaskNotificationPayload struct {
	TaskID    string `json:"task_id"`
	ToolUseID string `json:"tool_use_id"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
}

type TaskUpdatedPayload struct {
	TaskID string         `json:"task_id"`
	Patch  map[string]any `json:"patch"`
}

type HookStartedPayload struct {
	HookID    string `json:"hook_id"`
	HookEvent string `json:"hook_event"`
	HookName  string `json:"hook_name,omitempty"`
}

type HookResponsePayload struct {
	HookID    string `json:"hook_id"`
	HookEvent string `json:"hook_event"`
	ExitCode  int    `json:"exit_code"`
	Outcome   string `json:"outcome,omitempty"`
}

type ToolDecisionPayload struct {
	ToolUseID string `json:"toolUseId"`
	Decision  string `json:"decision"` // "allow" | "deny"
	Reason    string `json:"reason,omitempty"`
}

type TurnStartedPayload struct {
	ClientNonce string `json:"clientNonce"`
	TurnID      string `json:"turnId"`
	Prompt      string `json:"prompt"`
}

type ToolDecisionAppliedPayload struct {
	ToolUseID string `json:"toolUseId"`
	Decision  string `json:"decision"`
}

type ClaudeErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ClaudeRunEndedPayload struct {
	Message string `json:"message,omitempty"`
}
