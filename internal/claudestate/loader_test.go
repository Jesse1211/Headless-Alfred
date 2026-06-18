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
