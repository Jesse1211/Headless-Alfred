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

	// claudehistory.Parse force-sets every jsonl turn to done=true,
	// so we can't rely on jsonl's done to detect a stale trailing
	// turn. Take the answer from the snapshot's trailing turn before
	// any merge and remember it.
	staleTrailing := snapOK && len(snap.Turns) > 0 && !snap.Turns[len(snap.Turns)-1].Done

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
	if staleTrailing {
		finalizeStaleTrailingTurn(state.Turns)
	}
	state.InFlight = computeInFlight(state.Turns)
	return state, nil
}

// finalizeStaleTrailingTurn closes off any trailing turn that was
// in-flight at the time of the last snapshot write. Server-as-truth
// keeps the runner in process memory only — once the server (or its
// runner) restarts, no events will ever arrive to mark the turn done.
// Without this, the frontend hydrates the half-finished turn and
// spins forever on "Claude is thinking..." with a timer that never
// stops. Mutates in place.
//
// Caller is expected to have decided staleness from the SNAPSHOT
// (not from the merged Done flag, which is force-true via
// claudehistory.Parse).
func finalizeStaleTrailingTurn(turns []ClaudeTurn) {
	n := len(turns)
	if n == 0 {
		return
	}
	last := &turns[n-1]
	last.Done = true
	last.IsError = true
	now := time.Now().UTC()
	last.FinishedAt = &now
	last.Blocks = append(last.Blocks, AssistantBlock{
		Kind: "text",
		Text: "Server restarted while this turn was running. The runner was killed; reply was not delivered.",
	})
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

func parseTimeOrZero(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
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

// mergeTurns implements the spec's merge rules:
//   - jsonl is the ordering / skeleton truth source (Prompt, StartedAt,
//     ExpandedPrompt, Thinking, block sequence, text content, tool
//     skeleton: Name, Input, Result, IsError).
//   - snapshot provides extended fields: Turn.FinishedAt, IsError,
//     TotalCostUsd, Usage, Done (for the trailing turn);
//     Tool.StartedAt, FinishedAt, Decision, BgTaskID.
//   - Turns present in snapshot but absent from jsonl are dropped.
//   - Tool blocks present in snapshot but absent from jsonl are dropped.
func mergeTurns(jsonl, snap []ClaudeTurn) []ClaudeTurn {
	if len(jsonl) == 0 {
		return jsonl
	}
	snapByID := make(map[string]ClaudeTurn, len(snap))
	for _, t := range snap {
		snapByID[t.ID] = t
	}
	out := make([]ClaudeTurn, len(jsonl))
	for i, jt := range jsonl {
		st, ok := snapByID[jt.ID]
		if !ok {
			out[i] = jt
			continue
		}
		out[i] = mergeOneTurn(jt, st)
	}
	return out
}

func mergeOneTurn(jt, st ClaudeTurn) ClaudeTurn {
	// jsonl skeleton.
	out := jt
	// snapshot per-turn extensions.
	if st.FinishedAt != nil {
		ft := *st.FinishedAt
		out.FinishedAt = &ft
	}
	out.IsError = st.IsError
	if st.TotalCostUsd != nil {
		c := *st.TotalCostUsd
		out.TotalCostUsd = &c
	}
	if st.Usage != nil {
		u := *st.Usage
		out.Usage = &u
	}
	// For the trailing turn, the snapshot's Done wins (it may legitimately
	// be false while jsonl forces it true). For all other turns the jsonl's
	// "true" is correct because there is by definition a successor row.
	out.Done = st.Done || jt.Done
	out.Blocks = mergeBlocks(jt.Blocks, st.Blocks)
	return out
}

func mergeBlocks(jsonl, snap []AssistantBlock) []AssistantBlock {
	snapToolByID := map[string]ClaudeToolCall{}
	for _, b := range snap {
		if b.Kind == "tool" && b.Tool != nil {
			snapToolByID[b.Tool.ToolUseID] = *b.Tool
		}
	}
	out := make([]AssistantBlock, len(jsonl))
	for i, b := range jsonl {
		if b.Kind == "text" {
			out[i] = b
			continue
		}
		if b.Tool == nil {
			out[i] = b
			continue
		}
		merged := *b.Tool
		if sn, ok := snapToolByID[b.Tool.ToolUseID]; ok {
			merged.StartedAt = sn.StartedAt
			merged.FinishedAt = sn.FinishedAt
			merged.Decision = sn.Decision
			merged.BgTaskID = sn.BgTaskID
		}
		out[i] = AssistantBlock{Kind: "tool", Tool: &merged}
	}
	return out
}

// computeInFlight derives the InFlight flag from the trailing turn.
func computeInFlight(turns []ClaudeTurn) bool {
	n := len(turns)
	return n > 0 && !turns[n-1].Done
}
