package claudestate

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/jesseliu/headless-alfred/internal/claudehistory"
)

// Load reads the snapshot file (if present and valid) and the jsonl
// transcript (if present), and merges them into a ClaudeState. The
// failure matrix:
//
//	snapshot OK, jsonl OK     -> merge per Section 4 (Task 11)
//	snapshot OK, no jsonl     -> snapshot.Turns straight through
//	no snapshot, jsonl OK     -> pure jsonl rebuild (current behavior)
//	snapshot corrupt/version  -> WARN log + treat as missing
//	neither                   -> empty ClaudeState
//
// Always returns a non-nil ClaudeState; errors are only returned for
// unrecoverable I/O failures, not "expected missing file."
func Load(snapshotPath, jsonlPath string) (ClaudeState, error) {
	snap, snapOK := loadSnapshot(snapshotPath)
	jsonlTurns, jsonlOK := loadJsonl(jsonlPath)

	state := EmptyClaudeState()
	state.TurnsLoaded = true

	switch {
	case snapOK && jsonlOK:
		state.Turns = mergeTurns(jsonlTurns, snap.Turns)
	case snapOK && !jsonlOK:
		state.Turns = snap.Turns
	case !snapOK && jsonlOK:
		state.Turns = jsonlTurns
	default:
		// Both missing — empty state.
	}
	state.InFlight = computeInFlight(state.Turns)
	return state, nil
}

// loadSnapshot tries to read + validate the snapshot file. Returns
// (snapshotFile, true) on success, (zero, false) on any failure that
// should fall back to jsonl-only.
func loadSnapshot(path string) (snapshotFile, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("claudestate: snapshot read failed; falling back to jsonl",
				"path", path, "err", err)
		}
		return snapshotFile{}, false
	}
	var snap snapshotFile
	if err := json.Unmarshal(data, &snap); err != nil {
		slog.Warn("claudestate: snapshot corrupted; falling back to jsonl",
			"path", path, "err", err)
		return snapshotFile{}, false
	}
	if snap.Version != snapshotVersion {
		slog.Warn("claudestate: snapshot version mismatch; falling back to jsonl",
			"want", snapshotVersion, "got", snap.Version)
		return snapshotFile{}, false
	}
	return snap, true
}

// loadJsonl runs claudehistory.Parse and adapts its Turn shape to
// our ClaudeTurn. Returns (turns, true) on success; (nil, false) when
// the file is missing.
func loadJsonl(path string) ([]ClaudeTurn, bool) {
	turns, err := claudehistory.Parse(path, 0, "")
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("claudestate: jsonl parse failed",
				"path", path, "err", err)
		}
		return nil, false
	}
	if len(turns) == 0 {
		return nil, false
	}
	out := make([]ClaudeTurn, len(turns))
	for i, t := range turns {
		out[i] = adaptHistoryTurn(t)
	}
	return out, true
}

// adaptHistoryTurn maps claudehistory.Turn into ClaudeTurn. The
// history package's ToolCall has no startedAt/finishedAt/decision —
// those stay zero/empty until the merge step overlays them from a
// snapshot.
func adaptHistoryTurn(h claudehistory.Turn) ClaudeTurn {
	out := ClaudeTurn{
		ID:             h.ID,
		Prompt:         h.Prompt,
		ExpandedPrompt: h.ExpandedPrompt,
		StartedAt:      parseTimeOrZero(h.StartedAt),
		FinishedAt:     parseTimeOrNil(h.EndedAt),
		Thinking:       append([]string(nil), h.Thinking...),
		Done:           h.Done,
		Blocks:         make([]AssistantBlock, 0, len(h.Blocks)),
	}
	for _, b := range h.Blocks {
		switch b.Kind {
		case "text":
			out.Blocks = append(out.Blocks, AssistantBlock{Kind: "text", Text: b.Text})
		case "tool":
			if b.Tool == nil {
				continue
			}
			out.Blocks = append(out.Blocks, AssistantBlock{
				Kind: "tool",
				Tool: &ClaudeToolCall{
					ToolUseID: b.Tool.ToolUseID,
					Name:      b.Tool.Name,
					Input:     decodeJSONRaw(b.Tool.Input),
					Decision:  emptyToAllow(b.Tool.Decision),
					Result:    b.Tool.Result,
					IsError:   b.Tool.IsError,
				},
			})
		}
	}
	return out
}

// emptyToAllow normalises the history package's hardcoded "allow"
// decision while still letting empty values pass through unchanged
// (so a downstream merge can override).
func emptyToAllow(d string) string {
	if d == "" {
		return "allow"
	}
	return d
}

// decodeJSONRaw turns a raw JSON byte slice from claudehistory into
// the loose `any` shape ClaudeToolCall.Input uses.
func decodeJSONRaw(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func parseTimeOrZero(s string) (out time.Time) {
	if s == "" {
		return
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return
	}
	return t
}

// parseTimeOrNil returns nil for empty/malformed inputs, &t otherwise.
// Used for the optional finishedAt/endedAt fields that the jsonl
// either has or hasn't.
func parseTimeOrNil(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

// mergeTurns is a stub for Task 11; in this task it just prefers the
// snapshot. Real merge rules in the next commit.
func mergeTurns(jsonl, snap []ClaudeTurn) []ClaudeTurn {
	if len(snap) > 0 {
		return snap
	}
	return jsonl
}

// computeInFlight derives the InFlight flag from the trailing turn.
func computeInFlight(turns []ClaudeTurn) bool {
	if n := len(turns); n > 0 && !turns[n-1].Done {
		return true
	}
	return false
}
