package claudestate

import (
	"encoding/json"
	"testing"
	"time"
)

// Each Event kind round-trips through JSON with its tag and payload
// intact. The wire format is `{ "kind": "...", "timestamp": "...",
// "payload": { ... } }` so the broadcast layer can forward any Event
// verbatim and the client reducer can dispatch on kind.
func TestEvent_JSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   Event
	}{
		{"text_delta", Event{
			Kind:      EventTextDelta,
			Timestamp: ts,
			Payload:   TextDeltaPayload{Index: 0, Text: "hello"},
		}},
		{"tool_use_start", Event{
			Kind:      EventToolUseStart,
			Timestamp: ts,
			Payload:   ToolUseStartPayload{Index: 1, ToolUseID: "tu_1", Name: "Bash"},
		}},
		{"tool_decision", Event{
			Kind:      EventToolDecision,
			Timestamp: ts,
			Payload:   ToolDecisionPayload{ToolUseID: "tu_1", Decision: "allow"},
		}},
		{"result", Event{
			Kind:      EventResult,
			Timestamp: ts,
			Payload:   ResultPayload{IsError: false, TotalCostUsd: 0.001, Result: "done"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			var out Event
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatalf("unmarshal: %v\n%s", err, b)
			}
			if out.Kind != tc.in.Kind {
				t.Errorf("kind: got %q want %q", out.Kind, tc.in.Kind)
			}
			if !out.Timestamp.Equal(tc.in.Timestamp) {
				t.Errorf("timestamp: got %v want %v", out.Timestamp, tc.in.Timestamp)
			}
			if out.Payload == nil {
				t.Errorf("payload nil")
			}
		})
	}
}
