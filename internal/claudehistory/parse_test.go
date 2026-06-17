package claudehistory

import (
	"path/filepath"
	"strings"
	"testing"
)

// joinText concatenates all text blocks in order. Test-only helper —
// production code iterates Blocks directly, but tests are simpler when
// they can assert "the assistant said X" without walking the union.
func joinText(t Turn) string {
	var b strings.Builder
	for _, blk := range t.Blocks {
		if blk.Kind == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// allTools collects tool blocks in order.
func allTools(t Turn) []ToolCall {
	var out []ToolCall
	for _, blk := range t.Blocks {
		if blk.Kind == "tool" && blk.Tool != nil {
			out = append(out, *blk.Tool)
		}
	}
	return out
}

func TestSplitInjectedPrompt(t *testing.T) {
	cases := []struct {
		in           string
		wantUser     string
		wantExpanded string
	}{
		{"hi", "hi", ""},
		// No separator: full text is user-visible, expanded empty.
		{"please tell me a joke", "please tell me a joke", ""},
		// With the composePromptText separator: user-visible part is
		// the prefix, expanded is the full original text.
		{"ls\n\n---\nAfter your reply, update summary at /data/...", "ls", "ls\n\n---\nAfter your reply, update summary at /data/..."},
		// Edge: separator at the very start = empty user, full expanded.
		{"\n\n---\ninjection only", "", "\n\n---\ninjection only"},
	}
	for _, c := range cases {
		u, e := splitInjectedPrompt(c.in)
		if u != c.wantUser || e != c.wantExpanded {
			t.Errorf("splitInjectedPrompt(%q) = (%q, %q), want (%q, %q)", c.in, u, e, c.wantUser, c.wantExpanded)
		}
	}
}

func TestParse_Empty(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "empty.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Errorf("want 0 turns, got %d", len(turns))
	}
}

func TestParse_SimpleUserAssistant(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "simple.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	if turns[0].Prompt != "hi" {
		t.Errorf("prompt = %q", turns[0].Prompt)
	}
	if got := joinText(turns[0]); got != "hello" {
		t.Errorf("text = %q", got)
	}
	if !turns[0].Done {
		t.Errorf("turn not marked done")
	}
	if turns[0].StartedAt != "2026-06-15T10:00:00.000Z" {
		t.Errorf("startedAt = %q", turns[0].StartedAt)
	}
}

func TestParse_ToolUseAndResult(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "tool_use.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	turn := turns[0]
	if got := joinText(turn); got != "readingdone" {
		t.Errorf("text = %q (want concatenated assistant text)", got)
	}
	tools := allTools(turn)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(tools))
	}
	tool := tools[0]
	if tool.ToolUseID != "t1" || tool.Name != "Read" {
		t.Errorf("tool id/name = %q/%q", tool.ToolUseID, tool.Name)
	}
	if tool.Decision != "allow" {
		t.Errorf("decision = %q (want allow)", tool.Decision)
	}
	if tool.Result != "the file contents" {
		t.Errorf("result = %q", tool.Result)
	}
	if tool.IsError {
		t.Errorf("isError = true (want false)")
	}
}

func TestParse_MultiTurnPagination(t *testing.T) {
	all, err := Parse(filepath.Join("testdata", "multi_turn.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 turns, got %d", len(all))
	}
	// Limit=2 returns last 2 turns.
	last2, err := Parse(filepath.Join("testdata", "multi_turn.jsonl"), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(last2) != 2 {
		t.Fatalf("want 2 turns, got %d", len(last2))
	}
	if last2[0].Prompt != "q2" || last2[1].Prompt != "q3" {
		t.Errorf("got prompts %q,%q want q2,q3", last2[0].Prompt, last2[1].Prompt)
	}
	// Before=id-of-q3 with limit=2 returns the 2 turns ending just before q3 → q1,q2.
	before := all[2].ID
	page, err := Parse(filepath.Join("testdata", "multi_turn.jsonl"), 2, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("want 2 turns, got %d", len(page))
	}
	if page[0].Prompt != "q1" || page[1].Prompt != "q2" {
		t.Errorf("got prompts %q,%q want q1,q2", page[0].Prompt, page[1].Prompt)
	}
}

func TestParse_UnknownTypesSkipped(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "unknown_types.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	if turns[0].Prompt != "hi" || joinText(turns[0]) != "hello" {
		t.Errorf("unexpected turn: %+v", turns[0])
	}
}

func TestParse_MalformedMidReturnsPartial(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "malformed_mid.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	// We get the first turn (q1/a1). q2 also starts after the bad line —
	// we keep parsing past errors, so we expect both. Empty turns (no
	// assistant content) for q2 are still emitted; only its prompt is set.
	if len(turns) < 1 {
		t.Fatalf("want at least 1 turn, got %d", len(turns))
	}
	if turns[0].Prompt != "q1" || joinText(turns[0]) != "a1" {
		t.Errorf("first turn lost: %+v", turns[0])
	}
}
