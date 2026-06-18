package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/claudestate"
)

// One inbound claude stream event applied via the manager updates the
// in-memory state. The same event also gets broadcast back to the client
// (with the server timestamp embedded in the Payload).
func TestWS_ClaudeEvent_RoutedThroughApply(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	st, err := mgr.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	st.BeginTurn("u1", "hi", time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC))

	cap := &writerCapture{}
	env := claudeEventEnvelope{
		sessionID: "sess1",
		kind:      claude.EventKind("text_delta"),
		payload:   json.RawMessage(`{"index":0,"text":"hello"}`),
	}

	dispatchClaudeStreamEvent(env, "uuid-1", mgr, cap.write)

	if len(cap.frames) != 1 {
		t.Fatalf("frames: %d", len(cap.frames))
	}
	if cap.frames[0].Type != "claude_event" || cap.frames[0].EventKind != "text_delta" {
		t.Errorf("frame: %+v", cap.frames[0])
	}
	st.View(func(s *claudestate.ClaudeState) {
		if s.Turns[0].Blocks[0].Text != "hello" {
			t.Errorf("state not updated: %+v", s.Turns[0].Blocks)
		}
	})
}

// Regression for "tools never render in UI": dispatch must accept a
// *claude.ToolUseStartEvent (the actual production payload type) and
// land its ToolUseID in server state via Apply. The envelope.payload
// in production is a concrete struct pointer from claude.parser, not
// a json.RawMessage — so this test mirrors that exactly.
func TestWS_ToolUseStart_FromConcreteStruct_LandsInState(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	st, _ := mgr.GetOrLoad("sess1", "uuid-1")
	st.BeginTurn("u1", "use a tool", time.Now().UTC())

	cap := &writerCapture{}
	env := claudeEventEnvelope{
		sessionID: "sess1",
		kind:      claude.KindToolUseStart,
		payload: &claude.ToolUseStartEvent{
			Index:     1,
			ToolUseID: "toolu_abc123",
			Name:      "Bash",
		},
	}
	dispatchClaudeStreamEvent(env, "uuid-1", mgr, cap.write)

	st.View(func(s *claudestate.ClaudeState) {
		if len(s.Turns[0].Blocks) != 1 {
			t.Fatalf("blocks = %d, want 1 tool block", len(s.Turns[0].Blocks))
		}
		b := s.Turns[0].Blocks[0]
		if b.Kind != "tool" || b.Tool == nil {
			t.Fatalf("block not tool: %+v", b)
		}
		if b.Tool.ToolUseID != "toolu_abc123" {
			t.Errorf("ToolUseID = %q, want toolu_abc123 (snake_case decoding broken)", b.Tool.ToolUseID)
		}
		if b.Tool.Name != "Bash" {
			t.Errorf("Name = %q, want Bash", b.Tool.Name)
		}
	})
}

// Inbound tool_decision message updates state AND emits
// tool_decision_applied so other connected tabs see the change.
func TestWS_ToolDecision_BroadcastsApplied(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	st, _ := mgr.GetOrLoad("sess1", "uuid-1")
	st.BeginTurn("u1", "hi", time.Now().UTC())
	mustNoErr(t, st.Apply(claudestate.Event{
		Kind:      claudestate.EventToolUseStart,
		Timestamp: time.Now().UTC(),
		Payload:   &claudestate.ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"},
	}))

	cap := &writerCapture{}
	dispatchToolDecision("sess1", "tu_1", "deny", "user said no", mgr, cap.write)

	if len(cap.frames) != 1 {
		t.Fatalf("frames: %d", len(cap.frames))
	}
	if cap.frames[0].Type != "tool_decision_applied" {
		t.Errorf("frame: %+v", cap.frames[0])
	}
	if cap.frames[0].Decision != "deny" || cap.frames[0].ToolUseID != "tu_1" {
		t.Errorf("frame fields: %+v", cap.frames[0])
	}
	st.View(func(s *claudestate.ClaudeState) {
		got := s.Turns[0].Blocks[0].Tool.Decision
		if got != "deny" {
			t.Errorf("decision: %q", got)
		}
	})
}

// writerCapture buffers OutMsgs sent via write — used by the dispatch
// helper tests to assert what got broadcast.
type writerCapture struct {
	frames []OutMsg
}

func (w *writerCapture) write(msg OutMsg) error {
	w.frames = append(w.frames, msg)
	return nil
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Regression for "WS drop leaves InFlight=true forever":
// applyClaudeRunEnded must finalize the trailing turn so that after
// runClientLoop's defer takeAll() kills leaked runners, the next
// reconnect's /claude-state hydrate doesn't keep showing
// "Claude is thinking…". This is the inner half of the disconnect
// cleanup path — the outer half (stopRun on each entry) needs
// real subprocesses, but the state-finalize step can be tested
// hermetically.
func TestWS_ApplyClaudeRunEnded_FinalizesInFlightTurn(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	st, _ := mgr.GetOrLoad("sess1", "uuid-1")
	st.BeginTurn("u1", "hi", time.Now().UTC())
	st.View(func(s *claudestate.ClaudeState) {
		if !s.InFlight {
			t.Fatal("precondition: InFlight should be true after BeginTurn")
		}
	})

	applyClaudeRunEnded("sess1", "client disconnected", mgr)

	st.View(func(s *claudestate.ClaudeState) {
		if s.InFlight {
			t.Error("InFlight stayed true after applyClaudeRunEnded")
		}
		if !s.Turns[0].Done {
			t.Error("trailing turn not Done")
		}
		if !s.Turns[0].IsError {
			t.Error("trailing turn not IsError — reconnect would render it as success")
		}
		if len(s.Turns[0].Blocks) == 0 {
			t.Error("expected synthetic message block for the user to see")
		}
	})
}

// Regression for "composer locked forever after claude binary missing":
// the optimistic in-flight turn registered by BeginTurn must be
// finalized when spawn fails, not left dangling until the next server
// restart. dispatchClaudeError runs the same finalize path the
// runner-died case uses and broadcasts a claude_error frame.
func TestWS_ClaudeError_FinalizesInFlightTurn(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	st, _ := mgr.GetOrLoad("sess1", "uuid-1")
	// Simulate dispatchClaudePromptBegin: optimistic turn registered.
	st.BeginTurn("u1", "hi", time.Now().UTC())
	st.View(func(s *claudestate.ClaudeState) {
		if !s.InFlight {
			t.Fatal("precondition: InFlight should be true after BeginTurn")
		}
	})

	cap := &writerCapture{}
	dispatchClaudeError("sess1", "claude_spawn_failed", "exec: claude not found", mgr, cap.write)

	if len(cap.frames) != 1 {
		t.Fatalf("frames: %d", len(cap.frames))
	}
	f := cap.frames[0]
	if f.Type != "claude_error" {
		t.Errorf("type: %q", f.Type)
	}
	if f.Code != "claude_spawn_failed" {
		t.Errorf("code: %q", f.Code)
	}
	if f.Timestamp == "" {
		t.Error("timestamp missing — frontend uses it for the turn's finishedAt")
	}

	st.View(func(s *claudestate.ClaudeState) {
		if s.InFlight {
			t.Error("InFlight stayed true — composer would never unlock")
		}
		if !s.Turns[0].Done {
			t.Error("turn not marked Done — frontend's spinner would never stop")
		}
		if !s.Turns[0].IsError {
			t.Error("turn not marked IsError — frontend wouldn't style it red")
		}
		if s.LastError == nil || s.LastError.Code != "claude_spawn_failed" {
			t.Errorf("LastError = %+v", s.LastError)
		}
	})
}

func TestWS_ClaudePrompt_EmitsTurnStarted(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())
	_, _ = mgr.GetOrLoad("sess1", "uuid-1")

	cap := &writerCapture{}
	turnID := dispatchClaudePromptBegin("sess1", "client-nonce-abc", "hi there", mgr, cap.write)

	if len(cap.frames) != 1 {
		t.Fatalf("frames: %d", len(cap.frames))
	}
	f := cap.frames[0]
	if f.Type != "turn_started" {
		t.Errorf("type: %q", f.Type)
	}
	if f.ClientNonce != "client-nonce-abc" {
		t.Errorf("nonce: %q", f.ClientNonce)
	}
	if f.TurnID == "" || turnID == "" || f.TurnID != turnID {
		t.Errorf("turnID inconsistent: frame=%q returned=%q", f.TurnID, turnID)
	}
}
