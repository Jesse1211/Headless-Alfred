// Package claudehistory reconstructs the per-session Claude UI chat
// transcript by reading Claude CLI's own jsonl log
// (~/.claude/projects/<dir>/<uuid>.jsonl). The CLI's file is the
// source of truth — we never persist a parallel copy.
package claudehistory

// Turn is one round of the conversation: the user's prompt plus
// everything Claude produced in response. JSON tags mirror the
// frontend's ClaudeTurn shape exactly so the handler can stream
// straight to the reducer.
type Turn struct {
	ID string `json:"id"`
	// Prompt is what the user typed (the visible bubble). When the
	// server injected a template body before piping to claude -p,
	// Prompt is the user's part only — the full text including the
	// injection lives in ExpandedPrompt.
	Prompt string `json:"prompt"`
	// ExpandedPrompt is the FULL text the server piped to claude -p
	// for this turn. Populated only when the jsonl-recorded prompt
	// contains the "\n\n---\n" separator composePromptText writes
	// between the user's message and the rendered template. When
	// equal to Prompt (no template injection happened), the frontend
	// hides the "Show full prompt" toggle.
	ExpandedPrompt string `json:"expandedPrompt,omitempty"`
	StartedAt      string `json:"startedAt"`
	// Blocks is the assistant's reply as an ORDERED list of text and
	// tool blocks in the exact order Claude streamed them. JSON tag
	// "blocks" matches the frontend ClaudeTurn.blocks shape.
	Blocks []Block `json:"blocks"`
	// Thinking is the assistant's extended-thinking content blocks
	// recovered from the assistant message snapshot. Each element is
	// one complete thinking block (markdown text). Separate from
	// Blocks because thinking is internal reasoning, not part of the
	// user-facing reply timeline.
	Thinking []string `json:"thinking,omitempty"`
	Done     bool     `json:"done"`
}

// Block is one item in a turn's reply timeline. Discriminated by
// Kind. Exactly one of Text or Tool is meaningful per Block.
type Block struct {
	Kind string    `json:"kind"` // "text" or "tool"
	Text string    `json:"text,omitempty"`
	Tool *ToolCall `json:"tool,omitempty"`
}

// ToolCall is one tool invocation inside a turn. `Decision` is
// always "allow" for rebuilt turns — a tool that ran (whether the
// PreToolUse hook allowed or denied it) appears here; the denial is
// visible in the matching ToolResult's content + IsError, not in
// the decision field.
type ToolCall struct {
	ToolUseID string  `json:"toolUseId"`
	Name      string  `json:"name"`
	// Input is the raw JSON of the tool's input arguments — kept as
	// json.RawMessage to avoid re-encoding what's already valid JSON,
	// and to match the frontend's `input?: unknown`.
	Input    JSONRaw `json:"input,omitempty"`
	Decision string  `json:"decision"`
	Result   string  `json:"result,omitempty"`
	IsError  bool    `json:"isError,omitempty"`
}

// JSONRaw aliases json.RawMessage to keep the import out of files
// that don't need it. Marshals/unmarshals as the underlying bytes.
type JSONRaw = []byte
