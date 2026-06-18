package claudestate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// JSON wire shape must be exactly camelCase (matches frontend ts mirror).
func TestClaudeTurn_JSONRoundTrip(t *testing.T) {
	in := ClaudeTurn{
		ID:             "u1",
		Prompt:         "hi",
		ExpandedPrompt: "hi\n\n---\ntpl",
		StartedAt:      time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC),
		FinishedAt:     timePtr(time.Date(2026, 6, 18, 7, 0, 5, 0, time.UTC)),
		Blocks: []AssistantBlock{
			{Kind: "text", Text: "hello"},
			{Kind: "tool", Tool: &ClaudeToolCall{
				ToolUseID:  "tu_1",
				Name:       "Bash",
				Decision:   "allow",
				StartedAt:  timePtr(time.Date(2026, 6, 18, 7, 0, 1, 0, time.UTC)),
				FinishedAt: timePtr(time.Date(2026, 6, 18, 7, 0, 2, 0, time.UTC)),
			}},
		},
		Thinking: []string{"reasoning"},
		Done:     true,
		IsError:  false,
		Usage:    &TokenUsage{InputTokens: 10, OutputTokens: 5},
	}
	cost := 0.0123
	in.TotalCostUsd = &cost

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// Spot-check camelCase keys appear:
	for _, want := range []string{
		`"id"`, `"prompt"`, `"expandedPrompt"`, `"startedAt"`, `"finishedAt"`,
		`"blocks"`, `"thinking"`, `"done"`, `"totalCostUsd"`, `"usage"`,
		`"inputTokens"`, `"outputTokens"`, `"toolUseId"`, `"decision"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing key in JSON: %s\nJSON: %s", want, s)
		}
	}
	// Round-trip identity:
	var out ClaudeTurn
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Prompt != in.Prompt {
		t.Errorf("round-trip lost data: %+v", out)
	}
	if len(out.Blocks) != 2 || out.Blocks[1].Tool.ToolUseID != "tu_1" {
		t.Errorf("blocks lost: %+v", out.Blocks)
	}
	if out.TotalCostUsd == nil || *out.TotalCostUsd != 0.0123 {
		t.Errorf("cost lost: %v", out.TotalCostUsd)
	}
}

func TestClaudeState_DeepCopy_Independent(t *testing.T) {
	cost := 0.5
	src := ClaudeState{
		Turns: []ClaudeTurn{{
			ID:     "u1",
			Prompt: "hi",
			Blocks: []AssistantBlock{
				{Kind: "text", Text: "hello"},
				{Kind: "tool", Tool: &ClaudeToolCall{ToolUseID: "tu_1", Name: "Bash"}},
			},
			TotalCostUsd: &cost,
			Usage:        &TokenUsage{InputTokens: 1},
		}},
		BgTasks: map[string]BgTask{"t1": {TaskID: "t1"}},
	}

	dst := src.DeepCopy()

	// Mutate dst — src must be unaffected.
	dst.Turns[0].Prompt = "changed"
	dst.Turns[0].Blocks[0].Text = "changed"
	dst.Turns[0].Blocks[1].Tool.Name = "Read"
	*dst.Turns[0].TotalCostUsd = 9.99
	dst.Turns[0].Usage.InputTokens = 99
	dst.BgTasks["t1"] = BgTask{TaskID: "mutated"}

	if src.Turns[0].Prompt != "hi" {
		t.Errorf("src prompt mutated: %q", src.Turns[0].Prompt)
	}
	if src.Turns[0].Blocks[0].Text != "hello" {
		t.Errorf("src text mutated: %q", src.Turns[0].Blocks[0].Text)
	}
	if src.Turns[0].Blocks[1].Tool.Name != "Bash" {
		t.Errorf("src tool mutated: %q", src.Turns[0].Blocks[1].Tool.Name)
	}
	if *src.Turns[0].TotalCostUsd != 0.5 {
		t.Errorf("src cost mutated: %v", *src.Turns[0].TotalCostUsd)
	}
	if src.Turns[0].Usage.InputTokens != 1 {
		t.Errorf("src usage mutated: %v", src.Turns[0].Usage.InputTokens)
	}
	if src.BgTasks["t1"].TaskID != "t1" {
		t.Errorf("src bgTasks mutated: %+v", src.BgTasks["t1"])
	}
}