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
	case EventMessageStart, EventMessageStop, EventTextBlockEnd, EventUnknown:
		// No payload of interest.
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
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
}

type ToolUseEndPayload struct {
	ToolUseID string `json:"toolUseId"`
	Input     any    `json:"input,omitempty"`
}

type ToolResultPayload struct {
	ToolUseID string `json:"toolUseId"`
	Content   string `json:"content"`
	IsError   bool   `json:"isError"`
}

type MessageDeltaPayload struct {
	Usage TokenUsage `json:"usage"`
}

type ResultPayload struct {
	IsError      bool    `json:"isError"`
	TotalCostUsd float64 `json:"totalCostUsd"`
	Result       string  `json:"result,omitempty"`
}

type TaskStartedPayload struct {
	TaskID      string `json:"taskId"`
	ToolUseID   string `json:"toolUseId"`
	Description string `json:"description"`
	TaskType    string `json:"taskType"`
}

type TaskNotificationPayload struct {
	TaskID    string `json:"taskId"`
	ToolUseID string `json:"toolUseId"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
}

type TaskUpdatedPayload struct {
	TaskID string         `json:"taskId"`
	Patch  map[string]any `json:"patch"`
}

type HookStartedPayload struct {
	HookID    string `json:"hookId"`
	HookEvent string `json:"hookEvent"`
	HookName  string `json:"hookName,omitempty"`
}

type HookResponsePayload struct {
	HookID    string `json:"hookId"`
	HookEvent string `json:"hookEvent"`
	ExitCode  int    `json:"exitCode"`
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
