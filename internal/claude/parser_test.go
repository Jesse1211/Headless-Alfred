package claude

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestParseStream_SimpleText replays a real `claude -p` capture of
// asking "Reply with the single word: pong" and asserts the parser
// yields the expected sequence.
//
// The fixture was captured live; the model returned the word "pong"
// in two text deltas ("p" + "ong").
func TestParseStream_SimpleText(t *testing.T) {
	data, err := os.ReadFile("testdata/simple_text_response.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := collect(t, data)

	// Expected kinds, in order, ignoring rate_limit and unknown noise.
	wantSeq := []EventKind{
		KindSystem,       // init
		KindSystem,       // status
		KindRateLimit,    // allowed
		KindMessageStart, // model name
		KindTextDelta,    // "p"
		KindTextDelta,    // "ong"
		KindTextBlockEnd, // content_block_stop
		KindMessageDelta, // usage
		KindMessageStop,
		KindResult, // total cost
	}
	gotKinds := dropTrivia(got)
	if len(gotKinds) != len(wantSeq) {
		t.Fatalf("kind sequence length = %d, want %d; got=%v", len(gotKinds), len(wantSeq), gotKinds)
	}
	for i, want := range wantSeq {
		if gotKinds[i] != want {
			t.Errorf("event %d: kind = %q, want %q", i, gotKinds[i], want)
		}
	}

	// Verify the text reassembles to "pong".
	var sb strings.Builder
	for _, ev := range got {
		if ev.TextDelta != nil {
			sb.WriteString(ev.TextDelta.Text)
		}
	}
	if sb.String() != "pong" {
		t.Errorf("assembled text = %q, want %q", sb.String(), "pong")
	}

	// Verify the result event carries the cost and session id.
	var result *ResultEvent
	for _, ev := range got {
		if ev.Result != nil {
			result = ev.Result
		}
	}
	if result == nil {
		t.Fatal("no result event")
	}
	if result.TotalCostUSD <= 0 {
		t.Errorf("total_cost_usd = %v, want > 0", result.TotalCostUSD)
	}
	if result.SessionID == "" {
		t.Error("session_id missing")
	}
	if result.IsError {
		t.Error("result.is_error = true")
	}
	if result.Result == "" {
		t.Error("result.result text empty")
	}
}

// TestParseStream_ToolUse replays a capture where Claude was asked
// to "Run ls /tmp using your Bash tool" with --permission-mode
// bypassPermissions. The model:
//  1. emits a thinking block
//  2. emits a tool_use call to Bash with input {"command":"ls /tmp"}
//  3. receives a tool_result (the listing)
//  4. emits the final text response
func TestParseStream_ToolUse(t *testing.T) {
	data, err := os.ReadFile("testdata/tool_use_response.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := collect(t, data)

	// Sanity counts.
	var (
		sawToolUseStart bool
		sawToolResult   bool
		sawResult       bool
		textChunks      int
	)
	for _, ev := range got {
		switch ev.Kind {
		case KindToolUseStart:
			sawToolUseStart = true
			if ev.ToolUseStart.Name != "Bash" {
				t.Errorf("tool_use_start name = %q, want Bash", ev.ToolUseStart.Name)
			}
			if ev.ToolUseStart.ToolUseID == "" {
				t.Error("tool_use_start id empty")
			}
		case KindToolResult:
			sawToolResult = true
			if ev.ToolResult.Content == "" {
				t.Error("tool_result content empty")
			}
		case KindResult:
			sawResult = true
		case KindTextDelta:
			textChunks++
		}
	}
	if !sawToolUseStart {
		t.Error("never saw a tool_use_start event")
	}
	if !sawToolResult {
		t.Error("never saw a tool_result event")
	}
	if !sawResult {
		t.Error("never saw the final result event")
	}
	if textChunks == 0 {
		t.Error("no text deltas — model never wrote a response")
	}
}

// TestParseStream_MalformedLineIsToleratedAsUnknown verifies that
// one bad line does not stop the stream; it should be reported as
// an UnknownEvent and parsing should continue.
func TestParseStream_MalformedLineIsToleratedAsUnknown(t *testing.T) {
	data := []byte(`{"type":"system","subtype":"init"}` + "\n" +
		`THIS IS NOT JSON` + "\n" +
		`{"type":"result","subtype":"success","total_cost_usd":0.5,"result":"ok","session_id":"s","is_error":false,"num_turns":1,"duration_ms":1}` + "\n")
	got := collect(t, data)
	if len(got) != 3 {
		t.Fatalf("event count = %d, want 3", len(got))
	}
	if got[0].Kind != KindSystem {
		t.Errorf("event[0] kind = %q, want system", got[0].Kind)
	}
	if got[1].Kind != KindUnknown {
		t.Errorf("event[1] kind = %q, want unknown", got[1].Kind)
	}
	if got[1].Unknown == nil || got[1].Unknown.Type != "parse_error" {
		t.Errorf("event[1] unknown type = %v, want parse_error", got[1].Unknown)
	}
	if got[2].Kind != KindResult {
		t.Errorf("event[2] kind = %q, want result", got[2].Kind)
	}
}

// TestParseStream_EmptyAndBlankLinesIgnored verifies whitespace-
// only input doesn't produce ghost events. The CLI does emit blank
// lines occasionally.
func TestParseStream_EmptyAndBlankLinesIgnored(t *testing.T) {
	data := []byte("\n   \n\n\t\n")
	got := collect(t, data)
	if len(got) != 0 {
		t.Errorf("got %d events from blank input, want 0", len(got))
	}
}

// TestParseStream_UnknownTopLevelTypeIsReported ensures forward-
// compat: if the CLI invents a new top-level type, we don't drop
// the line silently.
func TestParseStream_UnknownTopLevelTypeIsReported(t *testing.T) {
	data := []byte(`{"type":"future_event","payload":42}` + "\n")
	got := collect(t, data)
	if len(got) != 1 {
		t.Fatalf("event count = %d, want 1", len(got))
	}
	if got[0].Kind != KindUnknown {
		t.Errorf("kind = %q, want unknown", got[0].Kind)
	}
	if got[0].Unknown.Type != "future_event" {
		t.Errorf("unknown.type = %q, want future_event", got[0].Unknown.Type)
	}
}

// --- helpers ---

func collect(t *testing.T, data []byte) []Event {
	t.Helper()
	ch := ParseStream(bytes.NewReader(data))
	out := []Event{}
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// dropTrivia removes "uninteresting" events from a sequence so the
// test can assert on the meaningful order. We drop nothing
// currently, but the hook is here if we add per-line debug events.
func dropTrivia(in []Event) []EventKind {
	out := make([]EventKind, 0, len(in))
	for _, ev := range in {
		if ev.Kind == KindUnknown {
			continue
		}
		out = append(out, ev.Kind)
	}
	return out
}

func TestParseLifecycleEvents(t *testing.T) {
	data, err := os.ReadFile("testdata/lifecycle_events.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var events []Event
	for ev := range ParseStream(bytes.NewReader(data)) {
		events = append(events, ev)
	}
	// Expect exactly 6 events from the fixture, in order:
	// hook_started, hook_response, task_started, task_notification (in_progress),
	// task_updated, task_notification (completed)
	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d", len(events))
	}
	want := []EventKind{
		KindHookStarted, KindHookResponse,
		KindTaskStarted, KindTaskNotification,
		KindTaskUpdated, KindTaskNotification,
	}
	for i, w := range want {
		if events[i].Kind != w {
			t.Errorf("events[%d].Kind = %q, want %q", i, events[i].Kind, w)
		}
	}

	// hook_started details
	hs := events[0].HookStarted
	if hs == nil {
		t.Fatal("HookStarted nil")
	}
	if hs.HookEvent != "PreToolUse" {
		t.Errorf("HookEvent = %q, want PreToolUse", hs.HookEvent)
	}
	if hs.HookID != "16dabf04-4dff-46c8-89b9-4960c33444d7" {
		t.Errorf("HookID = %q", hs.HookID)
	}

	// hook_response details
	hr := events[1].HookResponse
	if hr == nil {
		t.Fatal("HookResponse nil")
	}
	if hr.ExitCode != 0 || hr.Outcome != "success" {
		t.Errorf("HookResponse exit=%d outcome=%q", hr.ExitCode, hr.Outcome)
	}

	// task_started details
	ts := events[2].TaskStarted
	if ts == nil {
		t.Fatal("TaskStarted nil")
	}
	if ts.TaskID != "bgchxrc64" {
		t.Errorf("TaskID = %q", ts.TaskID)
	}
	if ts.ToolUseID != "toolu_01X5pXsEBLzmJX2yAWsKQdcz" {
		t.Errorf("ToolUseID = %q", ts.ToolUseID)
	}
	if ts.Description != "emit two lines with 4s sleep" {
		t.Errorf("Description = %q", ts.Description)
	}
	if ts.TaskType != "local_bash" {
		t.Errorf("TaskType = %q", ts.TaskType)
	}

	// task_notification (in_progress)
	tn := events[3].TaskNotification
	if tn == nil {
		t.Fatal("TaskNotification[3] nil")
	}
	if tn.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", tn.Status)
	}

	// task_updated
	tu := events[4].TaskUpdated
	if tu == nil {
		t.Fatal("TaskUpdated nil")
	}
	if tu.TaskID != "bgchxrc64" {
		t.Errorf("TaskID = %q", tu.TaskID)
	}
	if tu.Patch.Status != "completed" {
		t.Errorf("Patch.Status = %q", tu.Patch.Status)
	}
	if tu.Patch.EndTime != 1781706910801 {
		t.Errorf("Patch.EndTime = %d", tu.Patch.EndTime)
	}

	// task_notification (completed)
	tn2 := events[5].TaskNotification
	if tn2 == nil {
		t.Fatal("TaskNotification[5] nil")
	}
	if tn2.Status != "completed" {
		t.Errorf("Status = %q, want completed", tn2.Status)
	}
}
