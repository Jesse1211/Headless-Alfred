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
