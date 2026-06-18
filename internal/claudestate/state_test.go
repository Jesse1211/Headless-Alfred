package claudestate

import (
	"testing"
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
