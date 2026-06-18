package claudestate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The whole refactor's central invariant: feeding the same event
// sequence through the live reducer and the load-from-disk path
// produces equivalent state.
func TestRefreshParity_GoldenPath(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "claude.json")
	jsonlPath := filepath.Join(dir, "transcript.jsonl")

	// Synthesize a minimal jsonl bracketing the events below. The
	// merger needs this to provide the skeleton; the Persister provides
	// the extension fields.
	must(t, os.WriteFile(jsonlPath, []byte(
		`{"type":"user","message":{"role":"user","content":"do a thing"},"uuid":"u1","timestamp":"2026-06-18T07:00:00.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello "},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok","is_error":false}]},"timestamp":"2026-06-18T07:00:03.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n",
	), 0o600))

	// Live path: feed events through Apply and let Persister write.
	live := NewSessionState("sess1", "uuid-1")
	live.BeginTurn("u1", "do a thing", tAt(7, 0, 0))
	persister, err := NewPersister(snapPath, live, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	live.AttachPersister(persister)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go persister.Run(ctx)

	for _, ev := range []Event{
		{Kind: EventMessageStart, Timestamp: tAt(7, 0, 1)},
		{Kind: EventTextDelta, Timestamp: tAt(7, 0, 1),
			Payload: &TextDeltaPayload{Index: 0, Text: "hello "}},
		{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 2),
			Payload: &ToolUseStartPayload{Index: 1, ToolUseID: "tu_1", Name: "Bash"}},
		{Kind: EventToolUseEnd, Timestamp: tAt(7, 0, 2),
			Payload: &ToolUseEndPayload{ToolUseID: "tu_1", Input: map[string]any{"command": "ls"}}},
		{Kind: EventToolDecision, Timestamp: time.Date(2026, 6, 18, 7, 0, 2, 500_000_000, time.UTC),
			Payload: &ToolDecisionPayload{ToolUseID: "tu_1", Decision: "allow"}},
		{Kind: EventToolResult, Timestamp: tAt(7, 0, 3),
			Payload: &ToolResultPayload{ToolUseID: "tu_1", Content: "ok"}},
		{Kind: EventMessageStart, Timestamp: tAt(7, 0, 4)},
		{Kind: EventTextDelta, Timestamp: tAt(7, 0, 4),
			Payload: &TextDeltaPayload{Index: 0, Text: "done"}},
		{Kind: EventMessageDelta, Timestamp: tAt(7, 0, 5),
			Payload: &MessageDeltaPayload{Usage: MessageDeltaUsage{InputTokens: 100, OutputTokens: 5}}},
		{Kind: EventResult, Timestamp: tAt(7, 0, 5),
			Payload: &ResultPayload{IsError: false, TotalCostUsd: 0.001}},
	} {
		must(t, live.Apply(ev))
	}
	must(t, persister.Flush(ctx))

	var liveState ClaudeState
	live.View(func(st *ClaudeState) { liveState = st.DeepCopy() })

	// Refresh path: discard live in-memory state and load from disk.
	loaded, err := Load(snapPath, jsonlPath)
	if err != nil {
		t.Fatal(err)
	}

	// Equivalence: same turn count, ids, blocks, extension fields.
	if !reflect.DeepEqual(liveState.Turns, loaded.Turns) {
		diagnoseTurnDivergence(t, liveState.Turns, loaded.Turns)
	}
	if liveState.InFlight != loaded.InFlight {
		t.Errorf("inFlight: live=%v loaded=%v", liveState.InFlight, loaded.InFlight)
	}
}

// diagnoseTurnDivergence provides field-level breakdowns when
// reflect.DeepEqual on the full Turns slice fails, so the exact
// divergence is immediately readable in the test output.
func diagnoseTurnDivergence(t *testing.T, live, loaded []ClaudeTurn) {
	t.Helper()
	if len(live) != len(loaded) {
		t.Fatalf("turn count: live=%d loaded=%d\n live:   %+v\n loaded: %+v",
			len(live), len(loaded), live, loaded)
	}
	for i, lt := range live {
		rt := loaded[i]
		if lt.ID != rt.ID {
			t.Errorf("turn %d ID: live=%q loaded=%q", i, lt.ID, rt.ID)
		}
		if lt.Prompt != rt.Prompt {
			t.Errorf("turn %d Prompt: live=%q loaded=%q", i, lt.Prompt, rt.Prompt)
		}
		if !lt.StartedAt.Equal(rt.StartedAt) {
			t.Errorf("turn %d StartedAt: live=%v loaded=%v", i, lt.StartedAt, rt.StartedAt)
		}
		lfa := lt.FinishedAt
		rfa := rt.FinishedAt
		switch {
		case lfa == nil && rfa != nil:
			t.Errorf("turn %d FinishedAt: live=nil loaded=%v", i, *rfa)
		case lfa != nil && rfa == nil:
			t.Errorf("turn %d FinishedAt: live=%v loaded=nil", i, *lfa)
		case lfa != nil && rfa != nil && !lfa.Equal(*rfa):
			t.Errorf("turn %d FinishedAt: live=%v loaded=%v", i, *lfa, *rfa)
		}
		if lt.Done != rt.Done {
			t.Errorf("turn %d Done: live=%v loaded=%v", i, lt.Done, rt.Done)
		}
		if lt.IsError != rt.IsError {
			t.Errorf("turn %d IsError: live=%v loaded=%v", i, lt.IsError, rt.IsError)
		}
		if len(lt.Blocks) != len(rt.Blocks) {
			t.Errorf("turn %d block count: live=%d loaded=%d\n live:   %+v\n loaded: %+v",
				i, len(lt.Blocks), len(rt.Blocks), lt.Blocks, rt.Blocks)
			continue
		}
		for j, lb := range lt.Blocks {
			rb := rt.Blocks[j]
			if lb.Kind != rb.Kind {
				t.Errorf("turn %d block %d Kind: live=%q loaded=%q", i, j, lb.Kind, rb.Kind)
				continue
			}
			if lb.Kind == "text" && lb.Text != rb.Text {
				t.Errorf("turn %d block %d Text: live=%q loaded=%q", i, j, lb.Text, rb.Text)
			}
			if lb.Kind == "tool" {
				if lb.Tool == nil || rb.Tool == nil {
					t.Errorf("turn %d block %d Tool nil mismatch", i, j)
					continue
				}
				compareTool(t, i, j, lb.Tool, rb.Tool)
			}
		}
	}
	// If we get here without t.Errorf, fail with the raw diff anyway.
	if !t.Failed() {
		t.Errorf("turn equivalence failed (deep-equal disagrees but field walk agreed).\n live:   %+v\n loaded: %+v", live, loaded)
	}
}

func compareTool(t *testing.T, turnIdx, blockIdx int, live, loaded *ClaudeToolCall) {
	t.Helper()
	pfx := func(f string) string {
		return "turn " + string(rune('0'+turnIdx)) + " block " + string(rune('0'+blockIdx)) + " tool." + f
	}
	if live.ToolUseID != loaded.ToolUseID {
		t.Errorf("%s: live=%q loaded=%q", pfx("ToolUseID"), live.ToolUseID, loaded.ToolUseID)
	}
	if live.Name != loaded.Name {
		t.Errorf("%s: live=%q loaded=%q", pfx("Name"), live.Name, loaded.Name)
	}
	if live.Decision != loaded.Decision {
		t.Errorf("%s: live=%q loaded=%q", pfx("Decision"), live.Decision, loaded.Decision)
	}
	if live.Result != loaded.Result {
		t.Errorf("%s: live=%q loaded=%q", pfx("Result"), live.Result, loaded.Result)
	}
	if live.IsError != loaded.IsError {
		t.Errorf("%s: live=%v loaded=%v", pfx("IsError"), live.IsError, loaded.IsError)
	}
	if live.BgTaskID != loaded.BgTaskID {
		t.Errorf("%s: live=%q loaded=%q", pfx("BgTaskID"), live.BgTaskID, loaded.BgTaskID)
	}
	// StartedAt
	lsa, rsa := live.StartedAt, loaded.StartedAt
	switch {
	case lsa == nil && rsa != nil:
		t.Errorf("%s: live=nil loaded=%v", pfx("StartedAt"), *rsa)
	case lsa != nil && rsa == nil:
		t.Errorf("%s: live=%v loaded=nil", pfx("StartedAt"), *lsa)
	case lsa != nil && rsa != nil && !lsa.Equal(*rsa):
		t.Errorf("%s: live=%v loaded=%v", pfx("StartedAt"), *lsa, *rsa)
	}
	// FinishedAt
	lfa, rfa := live.FinishedAt, loaded.FinishedAt
	switch {
	case lfa == nil && rfa != nil:
		t.Errorf("%s: live=nil loaded=%v", pfx("FinishedAt"), *rfa)
	case lfa != nil && rfa == nil:
		t.Errorf("%s: live=%v loaded=nil", pfx("FinishedAt"), *lfa)
	case lfa != nil && rfa != nil && !lfa.Equal(*rfa):
		t.Errorf("%s: live=%v loaded=%v", pfx("FinishedAt"), *lfa, *rfa)
	}
	// Input — normalize via JSON round-trip to handle map[string]any vs map[string]interface{}.
	if !reflect.DeepEqual(live.Input, loaded.Input) {
		t.Errorf("%s: live=%+v loaded=%+v", pfx("Input"), live.Input, loaded.Input)
	}
}
