package claudestate

import (
	"context"
	"log/slog"
	"testing"
)

// warnCapture is a slog.Handler that records WARN-level records so a test
// can assert an observability log line fired with the expected attrs.
type warnCapture struct {
	records []slog.Record
}

func (h *warnCapture) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= slog.LevelWarn
}

func (h *warnCapture) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func (h *warnCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCapture) WithGroup(_ string) slog.Handler       { return h }

// attr returns the value of the named attr on the record, and whether it
// was present.
func recordAttr(r slog.Record, key string) (slog.Value, bool) {
	var found bool
	var val slog.Value
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value
			found = true
			return false
		}
		return true
	})
	return val, found
}

// This file audits how the Claude state machine reacts to error /
// abnormal-termination inputs. The method is: directly construct the
// event the relevant code path would produce ("assume the hook fired"),
// Apply it, then assert the reaction is sane.
//
// Each test's header comment states whether it documents a GAP
// (expected to FAIL against current code — a real bug; the assertion
// encodes the CORRECT behavior so the failing test pins the bug) or a
// HANDLED/benign no-op (expected to PASS — confirms the no-op is
// intentional).
//
// Helpers must(t, err), tAt(h,m,sec) live in state_test.go (same
// package).

// ---------------------------------------------------------------------
// GAP cases — these SHOULD FAIL against current code.
// ---------------------------------------------------------------------

// TestApply_ResultBadPayload_DoesNotStrandTurn documents a GAP.
//
// When EventResult arrives with a wrong-typed Payload, Apply's type
// assertion fails and it returns an error WITHOUT touching the turn.
// The in-flight turn is then stranded forever: Done=false and
// InFlight=true with no backstop. The correct behavior is that a
// malformed terminator should still unwind the in-flight turn (or at
// minimum drop InFlight so the composer unlocks).
//
// Expected: FAIL against current code (turn stays Done=false /
// InFlight=true).
func TestApply_ResultBadPayload_DoesNotStrandTurn(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "prompt", tAt(7, 0, 0))

	err := s.Apply(Event{
		Kind:      EventResult,
		Timestamp: tAt(7, 0, 1),
		Payload:   "not a result",
	})
	if err == nil {
		t.Fatalf("expected Apply to error on wrong-typed result payload, got nil")
	}

	s.View(func(st *ClaudeState) {
		if len(st.Turns) != 1 {
			t.Fatalf("expected 1 turn, got %d", len(st.Turns))
		}
		turn := st.Turns[0]
		// CORRECT behavior: a malformed terminator must not strand the
		// turn in-flight forever. Either the turn is finalized (Done) or
		// the session is no longer InFlight (a backstop unlocked it).
		stranded := !turn.Done && st.InFlight
		if stranded {
			t.Errorf("GAP: turn stranded after bad result payload (Done=%v, InFlight=%v); "+
				"a malformed terminator should finalize the turn or drop InFlight",
				turn.Done, st.InFlight)
		}
	})
}

// TestApply_RunEndedBadPayload_DoesNotStrandTurn documents a GAP.
//
// Same shape as the result case but for EventClaudeRunEnded — which is
// the explicit "run ended without a clean result" terminator. If its
// payload is wrong-typed, Apply errors before calling finalizeInFlight,
// so the very event meant to unlock the composer instead leaves the
// turn stuck.
//
// Expected: FAIL against current code.
func TestApply_RunEndedBadPayload_DoesNotStrandTurn(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "prompt", tAt(7, 0, 0))

	err := s.Apply(Event{
		Kind:      EventClaudeRunEnded,
		Timestamp: tAt(7, 0, 1),
		Payload:   "not a run-ended payload",
	})
	if err == nil {
		t.Fatalf("expected Apply to error on wrong-typed run_ended payload, got nil")
	}

	s.View(func(st *ClaudeState) {
		turn := st.Turns[0]
		stranded := !turn.Done && st.InFlight
		if stranded {
			t.Errorf("GAP: turn stranded after bad run_ended payload (Done=%v, InFlight=%v); "+
				"the run-ended terminator should finalize the turn even when malformed",
				turn.Done, st.InFlight)
		}
	})
}

// TestApply_ClaudeErrorBadPayload_DoesNotStrandTurn documents a GAP.
//
// EventClaudeError is the error-path terminator: it should record
// LastError AND finalize the in-flight turn as an error. With a
// wrong-typed payload, Apply errors out before doing either, so an
// errored run leaves the turn stuck in-flight and records no error.
//
// Expected: FAIL against current code.
func TestApply_ClaudeErrorBadPayload_DoesNotStrandTurn(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "prompt", tAt(7, 0, 0))

	err := s.Apply(Event{
		Kind:      EventClaudeError,
		Timestamp: tAt(7, 0, 1),
		Payload:   "not a claude-error payload",
	})
	if err == nil {
		t.Fatalf("expected Apply to error on wrong-typed claude_error payload, got nil")
	}

	s.View(func(st *ClaudeState) {
		turn := st.Turns[0]
		stranded := !turn.Done && st.InFlight
		if stranded {
			t.Errorf("GAP: turn stranded after bad claude_error payload (Done=%v, InFlight=%v); "+
				"the error terminator should finalize the in-flight turn as an error",
				turn.Done, st.InFlight)
		}
	})
}

// TestApply_ToolResult_NoMatchingBlock_IsObservable documents a GAP.
//
// applyToolResult walks the current turn's blocks looking for a tool
// block with the matching ToolUseID. If none matches, the result is
// silently dropped: no error, no LastError, no recorded block. A tool
// result with nowhere to land is a real anomaly (out-of-order stream,
// lost tool_use_start) and should be observable somehow — at minimum
// the drop should be detectable from state.
//
// Expected: FAIL against current code (silently dropped, undetectable).
func TestApply_ToolResult_NoMatchingBlock_IsObservable(t *testing.T) {
	// ADR-002: a dropped tool_result must be observable. Per ADR-002 the
	// fix is a pure observability addition (a greppable slog.Warn), NOT a
	// state mutation — so we capture slog at WARN level and assert the log
	// fired with the toolUseId field. We also still accept the older
	// signals (an error or a state mutation) so this test is not weakened:
	// ANY of these making the drop detectable counts as observable.
	cap := &warnCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(cap))
	defer slog.SetDefault(prev)

	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "prompt", tAt(7, 0, 0))

	// No tool_use_start was applied, so there is no tool block with this
	// ID. The result has nowhere to land.
	err := s.Apply(Event{
		Kind:      EventToolResult,
		Timestamp: tAt(7, 0, 1),
		Payload: &ToolResultPayload{
			ToolUseID: "orphan-tool-id",
			Content:   "some result content",
			IsError:   true,
		},
	})

	// Signal (a): an error is one acceptable way to make the drop
	// observable.
	observable := err != nil

	// Signal (b): a greppable WARN log fired carrying the toolUseId.
	for _, r := range cap.records {
		if r.Level != slog.LevelWarn {
			continue
		}
		if v, ok := recordAttr(r, "toolUseId"); ok && v.String() == "orphan-tool-id" {
			observable = true
		}
	}

	// Signal (c): the orphan result landed somewhere we can find, or a
	// LastError was recorded.
	s.View(func(st *ClaudeState) {
		if st.LastError != nil {
			observable = true
		}
		for _, turn := range st.Turns {
			for _, b := range turn.Blocks {
				if b.Kind == "tool" && b.Tool != nil &&
					b.Tool.ToolUseID == "orphan-tool-id" {
					observable = true
				}
			}
		}
	})

	if !observable {
		t.Errorf("GAP: tool_result for unknown id was silently dropped — " +
			"no error, no WARN log, no LastError, no recorded block; the drop is undetectable")
	}
}

// ---------------------------------------------------------------------
// HANDLED / benign cases — these SHOULD PASS.
// ---------------------------------------------------------------------

// TestApply_ResultNoTurn_IsConsistent confirms HANDLED behavior.
//
// A well-formed result arriving with no turn started clears InFlight
// and returns without panic. applyResult sets InFlight=false up front,
// then bails on turn==nil. Leaving InFlight=false (not stuck true) is
// the consistent, benign outcome.
//
// Expected: PASS.
func TestApply_ResultNoTurn_IsConsistent(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")

	must(t, s.Apply(Event{
		Kind:      EventResult,
		Timestamp: tAt(7, 0, 1),
		Payload:   &ResultPayload{IsError: false},
	}))

	s.View(func(st *ClaudeState) {
		if st.InFlight {
			t.Errorf("expected InFlight=false after result with no turn, got true")
		}
		if len(st.Turns) != 0 {
			t.Errorf("expected no turns to be created, got %d", len(st.Turns))
		}
	})
}

// TestApply_TextDeltaNoTurn_NoOp confirms HANDLED behavior.
//
// A text_delta with no turn started is a no-op: applyTextDelta bails on
// turn==nil. No panic, no phantom turn.
//
// Expected: PASS.
func TestApply_TextDeltaNoTurn_NoOp(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")

	must(t, s.Apply(Event{
		Kind:      EventTextDelta,
		Timestamp: tAt(7, 0, 1),
		Payload:   &TextDeltaPayload{Index: 0, Text: "hello"},
	}))

	s.View(func(st *ClaudeState) {
		if len(st.Turns) != 0 {
			t.Errorf("expected no turn created by text_delta with no turn, got %d", len(st.Turns))
		}
		if st.InFlight {
			t.Errorf("expected InFlight=false, got true")
		}
	})
}

// TestApply_DoubleFinalize_FirstWins confirms HANDLED behavior.
//
// A clean result (IsError=false) finalizes the turn, then a late
// claude_run_ended arrives. finalizeInFlight bails on turn.Done, so the
// first terminator wins: the turn must stay IsError=false (run_ended
// does NOT flip a successfully-closed turn into an error).
//
// Expected: PASS.
func TestApply_DoubleFinalize_FirstWins(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "prompt", tAt(7, 0, 0))

	must(t, s.Apply(Event{
		Kind:      EventResult,
		Timestamp: tAt(7, 0, 1),
		Payload:   &ResultPayload{IsError: false},
	}))
	must(t, s.Apply(Event{
		Kind:      EventClaudeRunEnded,
		Timestamp: tAt(7, 0, 2),
		Payload:   &ClaudeRunEndedPayload{Message: "run ended"},
	}))

	s.View(func(st *ClaudeState) {
		turn := st.Turns[0]
		if !turn.Done {
			t.Errorf("expected turn Done=true, got false")
		}
		if turn.IsError {
			t.Errorf("expected turn IsError=false (first terminator wins), got true")
		}
		if st.InFlight {
			t.Errorf("expected InFlight=false, got true")
		}
	})
}

// TestApply_RateLimitEvent_NoOp confirms HANDLED behavior.
//
// EventRateLimit is an advisory frame the reducer deliberately ignores
// (Apply's default branch no-ops). Applying it mid-turn must not mutate
// the turn or InFlight.
//
// Expected: PASS.
func TestApply_RateLimitEvent_NoOp(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "prompt", tAt(7, 0, 0))

	must(t, s.Apply(Event{
		Kind:      EventRateLimit,
		Timestamp: tAt(7, 0, 1),
		Payload:   nil,
	}))

	s.View(func(st *ClaudeState) {
		turn := st.Turns[0]
		if turn.Done {
			t.Errorf("expected turn untouched (Done=false) after rate_limit, got Done=true")
		}
		if turn.IsError {
			t.Errorf("expected turn untouched (IsError=false) after rate_limit, got IsError=true")
		}
		if !st.InFlight {
			t.Errorf("expected InFlight to stay true after rate_limit no-op, got false")
		}
	})
}
