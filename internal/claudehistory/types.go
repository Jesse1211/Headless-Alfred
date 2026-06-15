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
	ID        string     `json:"id"`
	Prompt    string     `json:"prompt"`
	StartedAt string     `json:"startedAt"`
	Text      string     `json:"text"`
	Tools     []ToolCall `json:"tools"`
	Done      bool       `json:"done"`
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
