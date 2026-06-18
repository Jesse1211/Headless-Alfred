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

// Multi-message-turn block ordering regression. Anthropic stream-json
// resets content-block `index` to 0 on each message_start. A single
// Alfred turn often spans multiple assistant messages (text → tool_use
// → tool_result → next assistant message). The reducer must reset its
// per-turn index map on message_start so the next message's index=0
// opens a fresh block — otherwise text folds into the prior message's
// block and tools sink to the array tail.
func TestApply_MultiMessage_KeepsInterleavedOrder(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "do a thing", tAt(7, 0, 0))

	// Message 1
	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 1)}))
	must(t, s.Apply(Event{Kind: EventTextDelta, Timestamp: tAt(7, 0, 2),
		Payload: &TextDeltaPayload{Index: 0, Text: "first reply "}}))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 3),
		Payload: &ToolUseStartPayload{Index: 1, ToolUseID: "tu_1", Name: "Bash"}}))
	must(t, s.Apply(Event{Kind: EventToolResult, Timestamp: tAt(7, 0, 4),
		Payload: &ToolResultPayload{ToolUseID: "tu_1", Content: "ok"}}))
	// Message 2 — index counter resets server-side.
	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 5)}))
	must(t, s.Apply(Event{Kind: EventTextDelta, Timestamp: tAt(7, 0, 6),
		Payload: &TextDeltaPayload{Index: 0, Text: "second reply "}}))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 7),
		Payload: &ToolUseStartPayload{Index: 1, ToolUseID: "tu_2", Name: "Read"}}))

	s.View(func(st *ClaudeState) {
		got := blockSummary(st.Turns[0].Blocks)
		want := []string{
			"text:first reply ",
			"tool:tu_1",
			"text:second reply ",
			"tool:tu_2",
		}
		if !equalStrSlice(got, want) {
			t.Errorf("blocks order:\n got  %v\n want %v", got, want)
		}
	})
}

func TestApply_MultiMessage_KeepsThinkingBlocksSeparate(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "think hard", tAt(7, 0, 0))

	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 1)}))
	must(t, s.Apply(Event{Kind: EventThinkingDelta, Timestamp: tAt(7, 0, 2),
		Payload: &ThinkingDeltaPayload{Index: 0, Text: "thought A"}}))
	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 3)}))
	must(t, s.Apply(Event{Kind: EventThinkingDelta, Timestamp: tAt(7, 0, 4),
		Payload: &ThinkingDeltaPayload{Index: 0, Text: "thought B"}}))

	s.View(func(st *ClaudeState) {
		want := []string{"thought A", "thought B"}
		if !equalStrSlice(st.Turns[0].Thinking, want) {
			t.Errorf("thinking: got %v want %v", st.Turns[0].Thinking, want)
		}
	})
}

func blockSummary(blocks []AssistantBlock) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		if b.Kind == "tool" {
			out[i] = "tool:" + b.Tool.ToolUseID
		} else {
			out[i] = "text:" + b.Text
		}
	}
	return out
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

func TestApply_ToolUseEnd_PatchesInput(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 1),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"}}))
	must(t, s.Apply(Event{Kind: EventToolUseEnd, Timestamp: tAt(7, 0, 2),
		Payload: &ToolUseEndPayload{ToolUseID: "tu_1", Input: map[string]any{"command": "ls"}}}))
	s.View(func(st *ClaudeState) {
		in, _ := st.Turns[0].Blocks[0].Tool.Input.(map[string]any)
		if in["command"] != "ls" {
			t.Errorf("input: %+v", st.Turns[0].Blocks[0].Tool.Input)
		}
	})
}

func TestApply_MessageDelta_StoresUsage(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventMessageDelta, Timestamp: tAt(7, 0, 1),
		Payload: &MessageDeltaPayload{Usage: TokenUsage{InputTokens: 100, OutputTokens: 50}}}))
	s.View(func(st *ClaudeState) {
		if st.Turns[0].Usage == nil || st.Turns[0].Usage.InputTokens != 100 {
			t.Errorf("usage: %+v", st.Turns[0].Usage)
		}
	})
}

func TestApply_Result_FinalizesTurn(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventTextDelta, Timestamp: tAt(7, 0, 1),
		Payload: &TextDeltaPayload{Index: 0, Text: "hi"}}))
	must(t, s.Apply(Event{Kind: EventResult, Timestamp: tAt(7, 0, 5),
		Payload: &ResultPayload{IsError: false, TotalCostUsd: 0.001}}))
	s.View(func(st *ClaudeState) {
		if !st.Turns[0].Done {
			t.Error("not Done")
		}
		if st.Turns[0].FinishedAt == nil || !st.Turns[0].FinishedAt.Equal(tAt(7, 0, 5)) {
			t.Errorf("FinishedAt = %v", st.Turns[0].FinishedAt)
		}
		if st.Turns[0].TotalCostUsd == nil || *st.Turns[0].TotalCostUsd != 0.001 {
			t.Errorf("cost = %v", st.Turns[0].TotalCostUsd)
		}
		if st.InFlight {
			t.Error("InFlight still true")
		}
	})
}

func TestApply_ClaudeRunEnded_FinalizesInFlightAsError(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventClaudeRunEnded, Timestamp: tAt(7, 0, 9),
		Payload: &ClaudeRunEndedPayload{Message: "killed"}}))
	s.View(func(st *ClaudeState) {
		tt := st.Turns[0]
		if !tt.Done || !tt.IsError {
			t.Errorf("Done=%v IsError=%v", tt.Done, tt.IsError)
		}
		if tt.FinishedAt == nil || !tt.FinishedAt.Equal(tAt(7, 0, 9)) {
			t.Errorf("FinishedAt = %v", tt.FinishedAt)
		}
		// Synthetic text block surfaces the kill reason so the UI is
		// not blank when the runner died before any text arrived.
		if len(tt.Blocks) != 1 || tt.Blocks[0].Kind != "text" || tt.Blocks[0].Text != "killed" {
			t.Errorf("synthetic message: %+v", tt.Blocks)
		}
	})
}

func TestApply_TaskStarted_LinksToolBlockAndCreatesBgTask(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "monitor", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 1),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_mon", Name: "Monitor"}}))
	must(t, s.Apply(Event{Kind: EventTaskStarted, Timestamp: tAt(7, 0, 2),
		Payload: &TaskStartedPayload{
			TaskID: "task_x", ToolUseID: "tu_mon",
			Description: "tail logs", TaskType: "local_bash",
		}}))
	s.View(func(st *ClaudeState) {
		bt, ok := st.BgTasks["task_x"]
		if !ok {
			t.Fatal("bgTask not created")
		}
		if bt.Status != "in_progress" {
			t.Errorf("status = %q", bt.Status)
		}
		if !bt.StartedAt.Equal(tAt(7, 0, 2)) {
			t.Errorf("StartedAt = %v", bt.StartedAt)
		}
		// The tool block now points at the bgTask.
		tool := st.Turns[0].Blocks[0].Tool
		if tool.BgTaskID != "task_x" {
			t.Errorf("BgTaskID = %q", tool.BgTaskID)
		}
	})
}

func TestApply_HookSubagent_FIFOPair(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventHookStarted, Timestamp: tAt(7, 0, 1),
		Payload: &HookStartedPayload{HookID: "h_start_1", HookEvent: "SubagentStart"}}))
	must(t, s.Apply(Event{Kind: EventHookStarted, Timestamp: tAt(7, 0, 2),
		Payload: &HookStartedPayload{HookID: "h_start_2", HookEvent: "SubagentStart"}}))
	must(t, s.Apply(Event{Kind: EventHookResponse, Timestamp: tAt(7, 0, 5),
		Payload: &HookResponsePayload{HookID: "h_stop_X", HookEvent: "SubagentStop"}}))
	s.View(func(st *ClaudeState) {
		// Oldest in-progress subagent (h_start_1) should be marked finished.
		first := st.Subagents["h_start_1"]
		second := st.Subagents["h_start_2"]
		if first.FinishedAt == nil {
			t.Error("oldest subagent should be finished")
		}
		if second.FinishedAt != nil {
			t.Error("newer subagent should still be in progress")
		}
	})
}

func TestApply_ToolDecision_PatchesBlockAndDropsApproval(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 1),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"}}))
	// Seed a pending approval the way the server's tool_approval_request
	// handler will: append to the queue, then resolve via tool_decision.
	s.mu.Lock()
	s.state.Pending = append(s.state.Pending, ClaudeToolApproval{ToolUseID: "tu_1", Tool: "Bash"})
	s.mu.Unlock()

	must(t, s.Apply(Event{Kind: EventToolDecision, Timestamp: tAt(7, 0, 2),
		Payload: &ToolDecisionPayload{ToolUseID: "tu_1", Decision: "deny"}}))

	s.View(func(st *ClaudeState) {
		if len(st.Pending) != 0 {
			t.Errorf("pending not drained: %+v", st.Pending)
		}
		if st.Turns[0].Blocks[0].Tool.Decision != "deny" {
			t.Errorf("decision = %q", st.Turns[0].Blocks[0].Tool.Decision)
		}
	})
}
