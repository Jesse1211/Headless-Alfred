// Package claudestate holds the in-memory Claude UI state for one
// session plus the snapshot persistence machinery. It is the
// authoritative server-side model — anything rendered in the Claude
// chat UI is derived from one of its fields.
//
// Wire format: every public field has an explicit `json:"camelCase"`
// tag matching the TypeScript ClaudeState mirror in
// web/src/features/sessions/types.ts. The HTTP /claude-state endpoint,
// the on-disk snapshot.json, and the WS broadcast frames all use these
// identical names — no naming translation between layers.
package claudestate

import "time"

// ClaudeState is the full Claude UI state for one Alfred session.
// Persisted: only `Turns`. Everything else is either derived
// (InFlight), transient (Pending, LastError), or an external-resource
// reference (BgTasks, Subagents) that the server only trusts while it
// is observing the resource live. The Loader rebuilds derived fields
// after hydration; transient and external slots come up empty.
type ClaudeState struct {
	Turns            []ClaudeTurn             `json:"turns"`
	InFlight         bool                     `json:"inFlight"`
	Pending          []ClaudeToolApproval     `json:"pending"`
	PendingQuestions []ClaudeQuestion         `json:"pendingQuestions"`
	LastError        *ClaudeError             `json:"lastError,omitempty"`
	BgTasks          map[string]BgTask        `json:"bgTasks"`
	Subagents        map[string]SubagentEntry `json:"subagents"`
	TurnsLoaded      bool                     `json:"turnsLoaded"`
}

// ClaudeTurn is one round of the conversation. Field order in JSON
// mirrors the TS interface declaration order for diff readability.
type ClaudeTurn struct {
	ID             string `json:"id"`
	Prompt         string `json:"prompt"`
	ExpandedPrompt string `json:"expandedPrompt,omitempty"`
	StartedAt      time.Time `json:"startedAt"`
	// FinishedAt is nil until the turn ends. The frontend TypeScript
	// mirror is `finishedAt?: string`, so a nil pointer omits the key
	// from the wire (matches `undefined` in TS) instead of emitting the
	// zero ISO string "0001-01-01T00:00:00Z" that time.Time would
	// otherwise produce.
	FinishedAt   *time.Time       `json:"finishedAt,omitempty"`
	Blocks       []AssistantBlock `json:"blocks"`
	Thinking     []string         `json:"thinking,omitempty"`
	Done         bool             `json:"done"`
	IsError      bool             `json:"isError,omitempty"`
	// Outcome is the terminal state and the source of truth; Done and
	// IsError are derived from it (see outcome.go). "" == in progress.
	Outcome string `json:"outcome,omitempty"` // "completed" | "errored" | "aborted"
	// AbortReason is a machine-readable code, non-empty only for
	// errored/aborted turns. Codes: runner_killed | ws_disconnect |
	// server_shutdown | server_restart | spawn_failed | rate_limit.
	AbortReason  string           `json:"abortReason,omitempty"`
	TotalCostUsd *float64         `json:"totalCostUsd,omitempty"`
	Usage        *TokenUsage      `json:"usage,omitempty"`
}

// AssistantBlock is one item in a turn's reply timeline. Kind
// discriminates the union: "text" → Text valid; "tool" → Tool valid.
type AssistantBlock struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	Tool *ClaudeToolCall `json:"tool,omitempty"`
}

// ClaudeToolCall is one tool invocation inside a turn. StartedAt /
// FinishedAt / Decision / BgTaskID are server-stamped extensions that
// the Claude CLI's jsonl does not record — they live only in our
// snapshot.
type ClaudeToolCall struct {
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
	Input     any    `json:"input,omitempty"`
	Decision  string `json:"decision"` // "allow" | "deny" | "pending"
	// Outcome is the tool's terminal state. "" == not terminated.
	// "completed" | "errored" | "aborted" | "denied".
	Outcome string `json:"outcome,omitempty"`
	Result  string `json:"result,omitempty"`
	IsError bool   `json:"isError,omitempty"`
	// StartedAt/FinishedAt are pointers so a zero value omits the JSON
	// key (matching the TS mirror's optional fields). A nil StartedAt
	// means the snapshot didn't capture the tool's start (e.g. tool
	// blocks rebuilt from jsonl only, no live reducer involvement).
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	BgTaskID   string     `json:"bgTaskId,omitempty"`
}

// TokenUsage mirrors the assistant message.usage shape from Claude.
type TokenUsage struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

// ClaudeToolApproval is a pending tool-use awaiting user decision.
// Not persisted to snapshot; server re-pushes from in-memory queue
// after reconnect.
type ClaudeToolApproval struct {
	ToolUseID string `json:"toolUseId"`
	Tool      string `json:"tool"`
	Input     any    `json:"input,omitempty"`
}

// ClaudeQuestion is a pending AskUserQuestion invocation. Same
// transience as ClaudeToolApproval.
type ClaudeQuestion struct {
	ToolUseID string            `json:"toolUseId"`
	Questions []ClaudeQuestionQ `json:"questions"`
}

// ClaudeQuestionQ is one question inside an AskUserQuestion.
type ClaudeQuestionQ struct {
	Question    string                 `json:"question"`
	Header      string                 `json:"header"`
	MultiSelect bool                   `json:"multiSelect"`
	Options     []ClaudeQuestionOption `json:"options"`
}

// ClaudeQuestionOption is one selectable answer.
type ClaudeQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// ClaudeError is the last error surfaced to the user. Transient.
type ClaudeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BgTask tracks one Claude-CLI-spawned background task. Producers
// observed: Monitor's detached bash, Bash(run_in_background=true).
// Lifecycle is owned exclusively by the CLI: it spawns, monitors,
// and SIGKILLs in-flight tasks when the parent `claude -p` exits
// (status="killed"). alfred-server is the observer, not the parent.
//
// External-resource reference — not persisted in snapshot.json;
// re-derived on alfred-server restart from .jsonl replay (every
// in_progress task is forced to status="killed" with
// LastEventSummary="killed when server restarted"). See ADR-001,
// ADR-018, and DESIGN.md.
type BgTask struct {
	TaskID            string     `json:"taskId"`
	ToolUseID         string     `json:"toolUseId"`
	Description       string     `json:"description"`
	TaskType          string     `json:"taskType"`
	StartedAt         time.Time  `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	Status            string     `json:"status"` // "in_progress" | "completed" | "failed" | "killed" | "stopped"
	LastEventSummary  string     `json:"lastEventSummary,omitempty"`
	NotificationCount int        `json:"notificationCount"`
}

// SubagentEntry tracks one in-flight subagent. Same transience as
// BgTask.
type SubagentEntry struct {
	HookID     string     `json:"hookId"`
	AgentType  string     `json:"agentType,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// EmptyClaudeState returns a zero-value state with maps initialized.
// Use this instead of `ClaudeState{}` so JSON marshals nil slices as
// `[]` instead of `null` (the frontend reducer expects arrays).
func EmptyClaudeState() ClaudeState {
	return ClaudeState{
		Turns:            []ClaudeTurn{},
		Pending:          []ClaudeToolApproval{},
		PendingQuestions: []ClaudeQuestion{},
		BgTasks:          map[string]BgTask{},
		Subagents:        map[string]SubagentEntry{},
	}
}

// timePtr boxes a time.Time so it can land in *time.Time fields.
// Convenience for reducer code: turn.FinishedAt = timePtr(ts).
func timePtr(t time.Time) *time.Time { return &t }

// DeepCopy returns a fully-independent copy of the state. Hand-written
// per-struct (not via reflect or JSON round-trip) so it stays fast and
// preserves typed nil pointers / unexported intent. Called by the
// HTTP snapshot handler and the Persister's writeSnapshot to capture
// state under the read lock without holding it during marshal.
func (s ClaudeState) DeepCopy() ClaudeState {
	// Collections must round-trip non-nil so the JSON wire format
	// stays `[]` / `{}` (not `null`) — the frontend dereferences
	// .length / .keys on them.
	out := ClaudeState{
		Turns:            make([]ClaudeTurn, len(s.Turns)),
		Pending:          make([]ClaudeToolApproval, len(s.Pending)),
		PendingQuestions: make([]ClaudeQuestion, len(s.PendingQuestions)),
		BgTasks:          make(map[string]BgTask, len(s.BgTasks)),
		Subagents:        make(map[string]SubagentEntry, len(s.Subagents)),
		InFlight:         s.InFlight,
		TurnsLoaded:      s.TurnsLoaded,
	}
	for i, t := range s.Turns {
		out.Turns[i] = t.DeepCopy()
	}
	copy(out.Pending, s.Pending)
	copy(out.PendingQuestions, s.PendingQuestions)
	if s.LastError != nil {
		e := *s.LastError
		out.LastError = &e
	}
	for k, v := range s.BgTasks {
		cp := v
		if v.FinishedAt != nil {
			ft := *v.FinishedAt
			cp.FinishedAt = &ft
		}
		out.BgTasks[k] = cp
	}
	for k, v := range s.Subagents {
		cp := v
		if v.FinishedAt != nil {
			ft := *v.FinishedAt
			cp.FinishedAt = &ft
		}
		out.Subagents[k] = cp
	}
	return out
}

// DeepCopy returns an independent copy of the turn.
func (t ClaudeTurn) DeepCopy() ClaudeTurn {
	out := t
	out.Blocks = make([]AssistantBlock, len(t.Blocks))
	for i, b := range t.Blocks {
		out.Blocks[i] = b.DeepCopy()
	}
	if t.Thinking != nil {
		out.Thinking = append([]string(nil), t.Thinking...)
	}
	if t.FinishedAt != nil {
		v := *t.FinishedAt
		out.FinishedAt = &v
	}
	if t.TotalCostUsd != nil {
		v := *t.TotalCostUsd
		out.TotalCostUsd = &v
	}
	if t.Usage != nil {
		u := *t.Usage
		out.Usage = &u
	}
	return out
}

// DeepCopy returns an independent copy of the block. Tool pointer is
// cloned so caller mutations to one block don't leak. The Tool's
// time pointers are likewise cloned.
func (b AssistantBlock) DeepCopy() AssistantBlock {
	out := AssistantBlock{Kind: b.Kind, Text: b.Text}
	if b.Tool != nil {
		t := *b.Tool
		if b.Tool.StartedAt != nil {
			ts := *b.Tool.StartedAt
			t.StartedAt = &ts
		}
		if b.Tool.FinishedAt != nil {
			tf := *b.Tool.FinishedAt
			t.FinishedAt = &tf
		}
		out.Tool = &t
	}
	return out
}
