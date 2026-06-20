package claudestate

import (
	"testing"
	"time"
)

func tsAt(h, m int) time.Time { return time.Date(2026, 6, 20, h, m, 0, 0, time.UTC) }

func newTestSessionState(t *testing.T) *SessionState {
	t.Helper()
	return &SessionState{state: EmptyClaudeState()}
}

func TestSetTurnOutcome_DerivesDoneIsError(t *testing.T) {
	cases := []struct {
		outcome     string
		wantDone    bool
		wantIsError bool
	}{
		{"completed", true, false},
		{"errored", true, true},
		{"aborted", true, true},
	}
	for _, c := range cases {
		turn := &ClaudeTurn{ID: "u1"}
		setTurnOutcome(turn, c.outcome, "", tsAt(7, 0))
		if turn.Outcome != c.outcome {
			t.Errorf("%s: Outcome=%q", c.outcome, turn.Outcome)
		}
		if turn.Done != c.wantDone {
			t.Errorf("%s: Done=%v want %v", c.outcome, turn.Done, c.wantDone)
		}
		if turn.IsError != c.wantIsError {
			t.Errorf("%s: IsError=%v want %v", c.outcome, turn.IsError, c.wantIsError)
		}
		if turn.FinishedAt == nil {
			t.Errorf("%s: FinishedAt nil", c.outcome)
		}
	}
}

func TestSetTurnOutcome_FirstTerminatorWins(t *testing.T) {
	turn := &ClaudeTurn{ID: "u1"}
	setTurnOutcome(turn, "completed", "", tsAt(7, 0))
	setTurnOutcome(turn, "aborted", "runner_killed", tsAt(7, 5))
	if turn.Outcome != "completed" {
		t.Errorf("second terminator overwrote: Outcome=%q", turn.Outcome)
	}
	if turn.AbortReason != "" {
		t.Errorf("second terminator set AbortReason=%q", turn.AbortReason)
	}
}

func TestSetTurnOutcome_RecordsAbortReason(t *testing.T) {
	turn := &ClaudeTurn{ID: "u1"}
	setTurnOutcome(turn, "aborted", "ws_disconnect", tsAt(7, 0))
	if turn.AbortReason != "ws_disconnect" {
		t.Errorf("AbortReason=%q", turn.AbortReason)
	}
}

func TestSetToolOutcome_DerivesIsError(t *testing.T) {
	tool := &ClaudeToolCall{ToolUseID: "t1"}
	setToolOutcome(tool, "aborted", tsAt(7, 0))
	if tool.Outcome != "aborted" || !tool.IsError || tool.FinishedAt == nil {
		t.Errorf("got Outcome=%q IsError=%v FinishedAt=%v", tool.Outcome, tool.IsError, tool.FinishedAt)
	}
	// denied is a terminal state but NOT an error.
	tool2 := &ClaudeToolCall{ToolUseID: "t2"}
	setToolOutcome(tool2, "denied", tsAt(7, 0))
	if tool2.IsError {
		t.Errorf("denied should not be isError")
	}
}

func TestApplyResult_SetsCompletedOutcome(t *testing.T) {
	s := newTestSessionState(t)
	s.state.Turns = []ClaudeTurn{{ID: "u1", StartedAt: tsAt(7, 0)}}
	s.state.InFlight = true
	s.applyResult(&ResultPayload{IsError: false, Result: "ok"}, tsAt(7, 1))
	if s.state.Turns[0].Outcome != "completed" {
		t.Errorf("Outcome=%q want completed", s.state.Turns[0].Outcome)
	}
}

func TestApplyResult_SetsErroredOutcome(t *testing.T) {
	s := newTestSessionState(t)
	s.state.Turns = []ClaudeTurn{{ID: "u1", StartedAt: tsAt(7, 0)}}
	s.state.InFlight = true
	s.applyResult(&ResultPayload{IsError: true, Result: "boom"}, tsAt(7, 1))
	if s.state.Turns[0].Outcome != "errored" {
		t.Errorf("Outcome=%q want errored", s.state.Turns[0].Outcome)
	}
}

func TestFinalizeInFlight_SetsAbortedOutcome(t *testing.T) {
	s := newTestSessionState(t)
	s.state.Turns = []ClaudeTurn{{ID: "u1", StartedAt: tsAt(7, 0)}}
	s.state.InFlight = true
	s.finalizeInFlight("client disconnected", "ws_disconnect", "aborted", tsAt(7, 1))
	turn := s.state.Turns[0]
	if turn.Outcome != "aborted" || turn.AbortReason != "ws_disconnect" {
		t.Errorf("Outcome=%q AbortReason=%q", turn.Outcome, turn.AbortReason)
	}
	if s.state.InFlight {
		t.Error("InFlight should be false")
	}
}
