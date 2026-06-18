package claudestate

import (
	"testing"
	"time"
)

func TestSessionState_View_ReturnsCopy(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	// Seed one turn through the internal mutator (not Apply yet — that's
	// Task 5; we directly poke the state for this isolation test).
	s.mu.Lock()
	s.state.Turns = append(s.state.Turns, ClaudeTurn{ID: "u1", Prompt: "hi"})
	s.mu.Unlock()

	var captured ClaudeState
	s.View(func(st *ClaudeState) {
		captured = st.DeepCopy()
	})
	// Mutating the captured copy must not affect the session's state.
	captured.Turns[0].Prompt = "MUTATED"

	s.View(func(st *ClaudeState) {
		if st.Turns[0].Prompt != "hi" {
			t.Errorf("session state mutated through View: %q", st.Turns[0].Prompt)
		}
	})
}

// Apply(text_delta) appends text to the per-turn block at the given
// index. Reuses existing text blocks for the same index.
func TestApply_TextDelta_Accumulates(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "hi", time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC))

	must(t, s.Apply(Event{
		Kind: EventTextDelta, Timestamp: tAt(7, 0, 1),
		Payload: &TextDeltaPayload{Index: 0, Text: "hel"},
	}))
	must(t, s.Apply(Event{
		Kind: EventTextDelta, Timestamp: tAt(7, 0, 2),
		Payload: &TextDeltaPayload{Index: 0, Text: "lo"},
	}))

	s.View(func(st *ClaudeState) {
		if got := blockText(st, 0, 0); got != "hello" {
			t.Errorf("text = %q want hello", got)
		}
	})
}

// Apply(tool_use_start) pushes a tool block at the array tail and
// records the server-stamped StartedAt.
func TestApply_ToolUseStart_StampsStartedAt(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "use a tool", tAt(7, 0, 0))
	must(t, s.Apply(Event{
		Kind: EventToolUseStart, Timestamp: tAt(7, 0, 5),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"},
	}))
	s.View(func(st *ClaudeState) {
		b := st.Turns[0].Blocks[0]
		if b.Kind != "tool" || b.Tool == nil {
			t.Fatalf("expected tool block, got %+v", b)
		}
		if b.Tool.StartedAt == nil || !b.Tool.StartedAt.Equal(tAt(7, 0, 5)) {
			t.Errorf("StartedAt = %v want %v", b.Tool.StartedAt, tAt(7, 0, 5))
		}
		if b.Tool.Decision != "pending" {
			t.Errorf("Decision = %q want pending", b.Tool.Decision)
		}
	})
}

// Apply(tool_result) sets Result/IsError on the matching tool block
// and stamps FinishedAt from the event timestamp.
func TestApply_ToolResult_PatchesByID(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "use a tool", tAt(7, 0, 0))
	must(t, s.Apply(Event{
		Kind: EventToolUseStart, Timestamp: tAt(7, 0, 5),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"},
	}))
	must(t, s.Apply(Event{
		Kind: EventToolResult, Timestamp: tAt(7, 0, 9),
		Payload: &ToolResultPayload{ToolUseID: "tu_1", Content: "ok", IsError: false},
	}))
	s.View(func(st *ClaudeState) {
		b := st.Turns[0].Blocks[0]
		if b.Tool.Result != "ok" {
			t.Errorf("Result = %q", b.Tool.Result)
		}
		if b.Tool.FinishedAt == nil || !b.Tool.FinishedAt.Equal(tAt(7, 0, 9)) {
			t.Errorf("FinishedAt = %v", b.Tool.FinishedAt)
		}
	})
}

// ---- helpers ----

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func tAt(h, m, sec int) time.Time {
	return time.Date(2026, 6, 18, h, m, sec, 0, time.UTC)
}

func blockText(st *ClaudeState, turnIdx, blockIdx int) string {
	return st.Turns[turnIdx].Blocks[blockIdx].Text
}
