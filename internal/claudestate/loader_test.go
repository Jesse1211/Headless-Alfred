package claudestate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_NoSnapshot_NoJsonl_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(filepath.Join(dir, "missing.json"), filepath.Join(dir, "missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 0 {
		t.Errorf("turns: %+v", got.Turns)
	}
	if got.BgTasks == nil || got.Subagents == nil {
		t.Errorf("maps should be initialized: %+v", got)
	}
}

func TestLoad_SnapshotOnly_NoJsonl(t *testing.T) {
	dir := t.TempDir()
	snap := snapshotFile{
		Version:   snapshotVersion,
		SessionID: "sess1",
		WrittenAt: time.Now().UTC(),
		Turns: []ClaudeTurn{{
			ID:        "u1",
			Prompt:    "hi",
			StartedAt: tAt(7, 0, 0),
			Blocks:    []AssistantBlock{{Kind: "text", Text: "from snapshot"}},
			Done:      true,
		}},
	}
	writeJSON(t, filepath.Join(dir, "claude.json"), snap)

	got, err := Load(filepath.Join(dir, "claude.json"), filepath.Join(dir, "missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 || got.Turns[0].Blocks[0].Text != "from snapshot" {
		t.Errorf("turns: %+v", got.Turns)
	}
}

func TestLoad_CorruptSnapshot_FallsBackToJsonl(t *testing.T) {
	dir := t.TempDir()
	// Write garbage at the snapshot path.
	must(t, os.WriteFile(filepath.Join(dir, "claude.json"), []byte("{not json"), 0o600))
	// Provide a one-turn jsonl.
	jsonl := filepath.Join(dir, "transcript.jsonl")
	must(t, os.WriteFile(jsonl, []byte(
		`{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-06-18T07:00:00.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`+"\n",
	), 0o600))
	got, err := Load(filepath.Join(dir, "claude.json"), jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 || got.Turns[0].Prompt != "hi" {
		t.Errorf("fallback failed: %+v", got)
	}
}

func TestLoad_VersionMismatch_FallsBack(t *testing.T) {
	dir := t.TempDir()
	snap := snapshotFile{Version: 99, SessionID: "sess1"}
	writeJSON(t, filepath.Join(dir, "claude.json"), snap)
	got, err := Load(filepath.Join(dir, "claude.json"), filepath.Join(dir, "missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 0 {
		t.Errorf("version-mismatched snapshot should be ignored: %+v", got)
	}
}

// Regression: when the server restarts mid-turn (or its runner dies
// without firing a result event), the snapshot's trailing turn has
// done=false. After restart, Load must NOT return it as in-flight —
// the runner is gone and no events will ever arrive. Finalize it as
// an error so the frontend immediately shows a clean state instead
// of an eternal "Claude is thinking..." spinner.
func TestLoad_StaleInFlightTurn_Finalized(t *testing.T) {
	dir := t.TempDir()
	startedAt := tAt(7, 0, 0)
	snap := snapshotFile{
		Version:   snapshotVersion,
		SessionID: "sess1",
		WrittenAt: tAt(7, 5, 0),
		Turns: []ClaudeTurn{{
			ID:        "u1",
			Prompt:    "long task",
			StartedAt: startedAt,
			Blocks:    []AssistantBlock{{Kind: "text", Text: "working on it"}},
			Done:      false,
		}},
	}
	writeJSON(t, filepath.Join(dir, "claude.json"), snap)

	got, err := Load(filepath.Join(dir, "claude.json"), filepath.Join(dir, "missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 {
		t.Fatalf("turns: %d", len(got.Turns))
	}
	last := got.Turns[0]
	if !last.Done {
		t.Error("trailing turn should be finalized as done")
	}
	if !last.IsError {
		t.Error("trailing turn should be marked isError after restart")
	}
	if last.FinishedAt == nil {
		t.Error("trailing turn should have a finishedAt")
	}
	// Existing text block preserved; an error note is appended so the
	// user sees what happened instead of a half-finished reply.
	if len(last.Blocks) < 2 {
		t.Errorf("expected the original blocks plus an error note: %+v", last.Blocks)
	} else {
		errBlock := last.Blocks[len(last.Blocks)-1]
		if errBlock.Kind != "text" || errBlock.Text == "" {
			t.Errorf("last block should be a non-empty text note: %+v", errBlock)
		}
	}
	if got.InFlight {
		t.Error("InFlight must be false after stale finalize")
	}
}

// claudehistory.Parse always returns done=true. The trailing-turn
// stale detector must read done from the snapshot, not from the
// merged result — otherwise jsonl's optimism hides the runner death.
func TestLoad_StaleTrailingTurn_FinalizedDespiteJsonlDoneTrue(t *testing.T) {
	dir := t.TempDir()
	snap := snapshotFile{
		Version: snapshotVersion, SessionID: "sess1",
		WrittenAt: tAt(7, 5, 0),
		Turns: []ClaudeTurn{{
			ID:        "u1",
			Prompt:    "hi",
			StartedAt: tAt(7, 0, 0),
			Blocks:    []AssistantBlock{{Kind: "text", Text: "working"}},
			Done:      false, // ← snapshot says it's still in flight
		}},
	}
	writeJSON(t, filepath.Join(dir, "claude.json"), snap)

	jsonl := filepath.Join(dir, "transcript.jsonl")
	must(t, os.WriteFile(jsonl, []byte(
		`{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-06-18T07:00:00.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`+"\n",
	), 0o600))

	got, err := Load(filepath.Join(dir, "claude.json"), jsonl)
	if err != nil {
		t.Fatal(err)
	}
	last := got.Turns[0]
	if !last.IsError {
		t.Error("trailing turn should be flagged isError after restart even with jsonl present")
	}
	if last.FinishedAt == nil {
		t.Error("trailing turn should have finishedAt")
	}
}

func TestLoad_DoneTrailingTurn_Untouched(t *testing.T) {
	dir := t.TempDir()
	snap := snapshotFile{
		Version: snapshotVersion, SessionID: "sess1",
		WrittenAt: tAt(7, 5, 0),
		Turns: []ClaudeTurn{{
			ID:        "u1",
			Prompt:    "ok",
			StartedAt: tAt(7, 0, 0),
			Blocks:    []AssistantBlock{{Kind: "text", Text: "all good"}},
			Done:      true,
		}},
	}
	writeJSON(t, filepath.Join(dir, "claude.json"), snap)
	got, err := Load(filepath.Join(dir, "claude.json"), filepath.Join(dir, "missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	last := got.Turns[0]
	if last.IsError {
		t.Error("already-done turn should not be flagged as error")
	}
	if len(last.Blocks) != 1 {
		t.Errorf("blocks shouldn't be augmented for done turns: %+v", last.Blocks)
	}
}

func TestMergeTurns_FieldLevelOverride(t *testing.T) {
	jsonl := []ClaudeTurn{{
		ID:     "u1",
		Prompt: "from jsonl",
		Blocks: []AssistantBlock{
			{Kind: "text", Text: "hello"},
			{Kind: "tool", Tool: &ClaudeToolCall{
				ToolUseID: "tu_1", Name: "Bash", Decision: "allow",
				Result: "ok",
			}},
		},
		StartedAt: tAt(7, 0, 0),
		Done:      true,
	}}
	cost := 0.05
	snap := []ClaudeTurn{{
		ID:           "u1",
		Prompt:       "should-not-override",
		FinishedAt:   timePtr(tAt(7, 0, 5)),
		TotalCostUsd: &cost,
		IsError:      false,
		Blocks: []AssistantBlock{
			{Kind: "tool", Tool: &ClaudeToolCall{
				ToolUseID:  "tu_1",
				Decision:   "deny",
				StartedAt:  timePtr(tAt(7, 0, 1)),
				FinishedAt: timePtr(tAt(7, 0, 3)),
				BgTaskID:   "task_x",
			}},
		},
		Done: true,
	}}

	merged := mergeTurns(jsonl, snap)
	if len(merged) != 1 {
		t.Fatalf("len: %d", len(merged))
	}
	got := merged[0]
	// jsonl is authoritative for skeleton text.
	if got.Prompt != "from jsonl" {
		t.Errorf("Prompt: %q (should come from jsonl)", got.Prompt)
	}
	if got.Blocks[0].Kind != "text" || got.Blocks[0].Text != "hello" {
		t.Errorf("text block lost: %+v", got.Blocks[0])
	}
	// snapshot is authoritative for tool extension fields.
	tool := got.Blocks[1].Tool
	if tool.Decision != "deny" {
		t.Errorf("decision: %q (should come from snapshot)", tool.Decision)
	}
	if tool.StartedAt == nil || !tool.StartedAt.Equal(tAt(7, 0, 1)) {
		t.Errorf("StartedAt: %v", tool.StartedAt)
	}
	if tool.FinishedAt == nil || !tool.FinishedAt.Equal(tAt(7, 0, 3)) {
		t.Errorf("FinishedAt: %v", tool.FinishedAt)
	}
	if tool.BgTaskID != "task_x" {
		t.Errorf("BgTaskID: %q", tool.BgTaskID)
	}
	// jsonl is authoritative for tool skeleton (Name, Result).
	if tool.Name != "Bash" || tool.Result != "ok" {
		t.Errorf("tool skeleton overwritten: %+v", tool)
	}
	// snapshot wins on per-turn extension fields.
	if got.TotalCostUsd == nil || *got.TotalCostUsd != 0.05 {
		t.Errorf("cost: %v", got.TotalCostUsd)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(tAt(7, 0, 5)) {
		t.Errorf("FinishedAt: %v", got.FinishedAt)
	}
}

func TestMergeTurns_SnapshotExtraTurnsDropped(t *testing.T) {
	jsonl := []ClaudeTurn{{ID: "u1", Prompt: "from jsonl"}}
	snap := []ClaudeTurn{
		{ID: "u1", Prompt: "x"},
		{ID: "u2", Prompt: "ghost"},
	}
	merged := mergeTurns(jsonl, snap)
	if len(merged) != 1 {
		t.Errorf("ghost turn not dropped: %+v", merged)
	}
}

func TestMergeTurns_SnapshotExtraToolDropped(t *testing.T) {
	jsonl := []ClaudeTurn{{
		ID:     "u1",
		Blocks: []AssistantBlock{{Kind: "tool", Tool: &ClaudeToolCall{ToolUseID: "tu_1", Name: "Bash"}}},
	}}
	snap := []ClaudeTurn{{
		ID: "u1",
		Blocks: []AssistantBlock{
			{Kind: "tool", Tool: &ClaudeToolCall{ToolUseID: "tu_1", Decision: "deny"}},
			{Kind: "tool", Tool: &ClaudeToolCall{ToolUseID: "tu_GHOST", Decision: "allow"}},
		},
	}}
	merged := mergeTurns(jsonl, snap)
	if len(merged[0].Blocks) != 1 {
		t.Errorf("ghost tool not dropped: %+v", merged[0].Blocks)
	}
}

// TestLoader_RebuildBgTasksFromJsonl_MarksInProgressAsKilled checks that
// a task_started event with no task_updated terminator results in a BgTask
// with Status="killed" and LastEventSummary="killed when server restarted"
// after Load (ADR-018).
func TestLoader_RebuildBgTasksFromJsonl_MarksInProgressAsKilled(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "transcript.jsonl")
	// A minimal jsonl: one user turn followed by a system task_started event.
	// No task_updated terminator — task is still "in_progress" at server death.
	must(t, os.WriteFile(jsonl, []byte(
		`{"type":"user","message":{"role":"user","content":"run it"},"uuid":"u1","timestamp":"2026-06-18T07:00:00.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"starting"}]}}`+"\n"+
			`{"type":"system","subtype":"task_started","task_id":"task1","tool_use_id":"tu_1","description":"bg job","task_type":"local_bash"}`+"\n",
	), 0o600))

	got, err := Load(filepath.Join(dir, "missing.json"), jsonl)
	if err != nil {
		t.Fatal(err)
	}
	bt, ok := got.BgTasks["task1"]
	if !ok {
		t.Fatalf("BgTask 'task1' not found; BgTasks=%+v", got.BgTasks)
	}
	if bt.Status != "killed" {
		t.Errorf("Status: want 'killed', got %q", bt.Status)
	}
	if bt.LastEventSummary != "killed when server restarted" {
		t.Errorf("LastEventSummary: want 'killed when server restarted', got %q", bt.LastEventSummary)
	}
	if bt.FinishedAt == nil {
		t.Error("FinishedAt should be set after force-kill")
	}
}

// TestLoader_RebuildBgTasksFromJsonl_PreservesCompletedStatus checks that
// a task whose jsonl ends with task_updated(status=completed) is NOT
// overwritten to "killed" — only in_progress tasks get force-killed.
func TestLoader_RebuildBgTasksFromJsonl_PreservesCompletedStatus(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "transcript.jsonl")
	must(t, os.WriteFile(jsonl, []byte(
		`{"type":"user","message":{"role":"user","content":"run it"},"uuid":"u1","timestamp":"2026-06-18T07:00:00.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n"+
			`{"type":"system","subtype":"task_started","task_id":"task2","tool_use_id":"tu_2","description":"bg job","task_type":"local_bash"}`+"\n"+
			`{"type":"system","subtype":"task_updated","task_id":"task2","patch":{"status":"completed","end_time":1781706910801}}`+"\n",
	), 0o600))

	got, err := Load(filepath.Join(dir, "missing.json"), jsonl)
	if err != nil {
		t.Fatal(err)
	}
	bt, ok := got.BgTasks["task2"]
	if !ok {
		t.Fatalf("BgTask 'task2' not found; BgTasks=%+v", got.BgTasks)
	}
	if bt.Status != "completed" {
		t.Errorf("Status: want 'completed', got %q (should not be overwritten to killed)", bt.Status)
	}
	if bt.FinishedAt == nil {
		t.Error("FinishedAt should be set from task_updated end_time")
	}
}

// TestLoader_RebuildBgTasks_HonorsSubagentsNoRebuildRule checks that even
// when the jsonl contains SubagentStart/SubagentStop hook events, the
// Subagents map is empty after Load (ADR-009).
func TestLoader_RebuildBgTasks_HonorsSubagentsNoRebuildRule(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "transcript.jsonl")
	must(t, os.WriteFile(jsonl, []byte(
		`{"type":"user","message":{"role":"user","content":"spawn subagent"},"uuid":"u1","timestamp":"2026-06-18T07:00:00.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`+"\n"+
			`{"type":"system","subtype":"hook_started","hook_id":"h1","hook_event":"SubagentStart","hook_name":"subagent_hook"}`+"\n"+
			`{"type":"system","subtype":"hook_response","hook_id":"h1","hook_event":"SubagentStop","exit_code":0,"outcome":"success"}`+"\n",
	), 0o600))

	got, err := Load(filepath.Join(dir, "missing.json"), jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Subagents) != 0 {
		t.Errorf("Subagents should be empty after Load (ADR-009); got %+v", got.Subagents)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
