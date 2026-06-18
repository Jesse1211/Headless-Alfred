# Claude Refresh Parity — Plan 1/3: Server Foundation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `internal/claudestate` package — types, Persister with atomic snapshot writes, SessionState with the central `Apply` reducer, and Loader that merges snapshot + jsonl per the spec. Self-contained: nothing outside this package changes.

**Architecture:** New Go package `internal/claudestate`. Types mirror frontend `ClaudeState`/`ClaudeTurn`/`ClaudeToolCall` shape exactly (JSON wire identical). `SessionState.Apply` is the only mutation entry; a per-session `Persister` goroutine debounces dirty signals and writes the full turns array atomically. `Loader.Load` merges snapshot.json (extended fields) with `claudehistory.Parse` output (chat skeleton).

**Tech Stack:** Go 1.21+, stdlib only (no new deps). Existing `internal/claudehistory` package consumed for jsonl replay.

**Spec:** `docs/superpowers/specs/2026-06-18-claude-refresh-parity-design.md`

**Branch:** `refactor/refresh-parity` (already created off `next`)

---

## File Structure

| Path | Purpose |
|---|---|
| `internal/claudestate/types.go` | All public types: `ClaudeState`, `ClaudeTurn`, `ClaudeToolCall`, `AssistantBlock`, `Event` (tagged union), helpers (deepCopy). |
| `internal/claudestate/state.go` | `SessionState` struct, `Apply`, `View`, `Close`. The reducer body lives here. |
| `internal/claudestate/persister.go` | `Persister` goroutine, atomic write, flock, orphan tmp cleanup. |
| `internal/claudestate/loader.go` | `Load(snapshotPath, jsonlPath)` — reads snapshot.json, calls `claudehistory.Parse`, merges per the matrix. |
| `internal/claudestate/types_test.go` | Round-trip JSON marshal/unmarshal, deepCopy correctness. |
| `internal/claudestate/state_test.go` | Apply per event kind, multi-message ordering regression. |
| `internal/claudestate/persister_test.go` | Dirty → write timing, debounce, Flush, atomic-write, flock conflict, orphan cleanup. |
| `internal/claudestate/loader_test.go` | Merge rules, missing/corrupt snapshot fallback, trailing-turn `done` resolution, snapshot-only / jsonl-only paths. |
| `internal/claudestate/testdata/...` | Fixtures: paired `.jsonl` + `.snapshot.json` + `.events.json`. |

**Out of scope for this plan:** `SessionManager`, HTTP endpoint, WS integration, frontend. Plan 2 covers those. This plan delivers a pure library callable from tests.

---

## Task 1: Package skeleton + types

**Files:**
- Create: `internal/claudestate/types.go`
- Create: `internal/claudestate/types_test.go`

- [ ] **Step 1: Write the failing test**

```go
package claudestate

import (
	"encoding/json"
	"testing"
	"time"
)

// JSON wire shape must be exactly camelCase (matches frontend ts mirror).
func TestClaudeTurn_JSONRoundTrip(t *testing.T) {
	in := ClaudeTurn{
		ID:             "u1",
		Prompt:         "hi",
		ExpandedPrompt: "hi\n\n---\ntpl",
		StartedAt:  time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC),
		FinishedAt: timePtr(time.Date(2026, 6, 18, 7, 0, 5, 0, time.UTC)),
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
		Usage: &TokenUsage{InputTokens: 10, OutputTokens: 5},
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
		if !contains(s, want) {
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd /Users/jesseliu/Desktop/Chore/Headless-Alfred
go test ./internal/claudestate/...
```

Expected: build failure — `claudestate` package not found / types undefined.

- [ ] **Step 3: Write `types.go`**

```go
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
	Turns            []ClaudeTurn               `json:"turns"`
	InFlight         bool                       `json:"inFlight"`
	Pending          []ClaudeToolApproval       `json:"pending"`
	PendingQuestions []ClaudeQuestion           `json:"pendingQuestions"`
	LastError        *ClaudeError               `json:"lastError,omitempty"`
	BgTasks          map[string]BgTask          `json:"bgTasks"`
	Subagents        map[string]SubagentEntry   `json:"subagents"`
	TurnsLoaded      bool                       `json:"turnsLoaded"`
}

// ClaudeTurn is one round of the conversation. Field order in JSON
// mirrors the TS interface declaration order for diff readability.
type ClaudeTurn struct {
	ID             string           `json:"id"`
	Prompt         string           `json:"prompt"`
	ExpandedPrompt string           `json:"expandedPrompt,omitempty"`
	StartedAt      time.Time        `json:"startedAt"`
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
	ToolUseID  string    `json:"toolUseId"`
	Name       string    `json:"name"`
	Input      any       `json:"input,omitempty"`
	Decision   string     `json:"decision"` // "allow" | "deny" | "pending"
	Result     string     `json:"result,omitempty"`
	IsError    bool       `json:"isError,omitempty"`
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
	ToolUseID string             `json:"toolUseId"`
	Questions []ClaudeQuestionQ  `json:"questions"`
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

// BgTask tracks one CLI background task (Monitor's detached bash).
// External-resource reference — not persisted.
type BgTask struct {
	TaskID            string     `json:"taskId"`
	ToolUseID         string     `json:"toolUseId"`
	Description       string     `json:"description"`
	TaskType          string     `json:"taskType"`
	StartedAt         time.Time  `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	Status            string     `json:"status"` // "in_progress" | "completed" | "failed"
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
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -run TestClaudeTurn_JSONRoundTrip -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/types.go internal/claudestate/types_test.go
git commit -m "feat(claudestate): public types with wire-compatible JSON tags

types.go declares ClaudeState, ClaudeTurn, ClaudeToolCall and helpers
with explicit camelCase json tags. Field shape and key names exactly
mirror the TypeScript ClaudeState in web/src/features/sessions/types.ts
so the HTTP, WS, and on-disk snapshot layers all share one schema.

Refs: docs/superpowers/specs/2026-06-18-claude-refresh-parity-design.md

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 2: deepCopy helper

**Files:**
- Modify: `internal/claudestate/types.go` (append `DeepCopy` methods)
- Modify: `internal/claudestate/types_test.go` (add copy test)

- [ ] **Step 1: Write the failing test**

Append to `types_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestClaudeState_DeepCopy_Independent -v
```

Expected: FAIL — `DeepCopy` undefined.

- [ ] **Step 3: Implement `DeepCopy` methods**

Append to `types.go`:

```go
// DeepCopy returns a fully-independent copy of the state. Hand-written
// per-struct (not via reflect or JSON round-trip) so it stays fast and
// preserves typed nil pointers / unexported intent. Called by the
// HTTP snapshot handler and the Persister's writeSnapshot to capture
// state under the read lock without holding it during marshal.
func (s ClaudeState) DeepCopy() ClaudeState {
	out := ClaudeState{
		Turns:            make([]ClaudeTurn, len(s.Turns)),
		Pending:          append([]ClaudeToolApproval(nil), s.Pending...),
		PendingQuestions: append([]ClaudeQuestion(nil), s.PendingQuestions...),
		InFlight:         s.InFlight,
		TurnsLoaded:      s.TurnsLoaded,
	}
	for i, t := range s.Turns {
		out.Turns[i] = t.DeepCopy()
	}
	if s.LastError != nil {
		e := *s.LastError
		out.LastError = &e
	}
	if s.BgTasks != nil {
		out.BgTasks = make(map[string]BgTask, len(s.BgTasks))
		for k, v := range s.BgTasks {
			cp := v
			if v.FinishedAt != nil {
				ft := *v.FinishedAt
				cp.FinishedAt = &ft
			}
			out.BgTasks[k] = cp
		}
	}
	if s.Subagents != nil {
		out.Subagents = make(map[string]SubagentEntry, len(s.Subagents))
		for k, v := range s.Subagents {
			cp := v
			if v.FinishedAt != nil {
				ft := *v.FinishedAt
				cp.FinishedAt = &ft
			}
			out.Subagents[k] = cp
		}
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
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -run TestClaudeState_DeepCopy_Independent -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/types.go internal/claudestate/types_test.go
git commit -m "feat(claudestate): hand-written DeepCopy for state/turn/block

Avoids reflect (slow) and JSON round-trip (silently drops json:\"-\"
fields, and we will gain some when index-map bookkeeping migrates in).
Used by the HTTP snapshot handler and the Persister to capture state
under read lock then marshal outside the lock.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 3: Event taxonomy

**Files:**
- Create: `internal/claudestate/events.go`
- Create: `internal/claudestate/events_test.go`

- [ ] **Step 1: Write the failing test**

```go
package claudestate

import (
	"encoding/json"
	"testing"
	"time"
)

// Each Event kind round-trips through JSON with its tag and payload
// intact. The wire format is `{ "kind": "...", "timestamp": "...",
// "payload": { ... } }` so the broadcast layer can forward any Event
// verbatim and the client reducer can dispatch on kind.
func TestEvent_JSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   Event
	}{
		{"text_delta", Event{
			Kind:      EventTextDelta,
			Timestamp: ts,
			Payload:   TextDeltaPayload{Index: 0, Text: "hello"},
		}},
		{"tool_use_start", Event{
			Kind:      EventToolUseStart,
			Timestamp: ts,
			Payload:   ToolUseStartPayload{Index: 1, ToolUseID: "tu_1", Name: "Bash"},
		}},
		{"tool_decision", Event{
			Kind:      EventToolDecision,
			Timestamp: ts,
			Payload:   ToolDecisionPayload{ToolUseID: "tu_1", Decision: "allow"},
		}},
		{"result", Event{
			Kind:      EventResult,
			Timestamp: ts,
			Payload:   ResultPayload{IsError: false, TotalCostUsd: 0.001, Result: "done"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			var out Event
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatalf("unmarshal: %v\n%s", err, b)
			}
			if out.Kind != tc.in.Kind {
				t.Errorf("kind: got %q want %q", out.Kind, tc.in.Kind)
			}
			if !out.Timestamp.Equal(tc.in.Timestamp) {
				t.Errorf("timestamp: got %v want %v", out.Timestamp, tc.in.Timestamp)
			}
			if out.Payload == nil {
				t.Errorf("payload nil")
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestEvent_JSONRoundTrip -v
```

Expected: FAIL — `Event` undefined.

- [ ] **Step 3: Write `events.go`**

```go
package claudestate

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventKind tags one variant of the Event union. Values are kept
// identical to the WS frame `eventKind` field the frontend already
// uses, so a single string drives both the Go reducer dispatch and
// the TS reducer dispatch.
type EventKind string

const (
	EventMessageStart   EventKind = "message_start"
	EventMessageDelta   EventKind = "message_delta"
	EventMessageStop    EventKind = "message_stop"
	EventTextDelta      EventKind = "text_delta"
	EventThinkingDelta  EventKind = "thinking_delta"
	EventTextBlockEnd   EventKind = "text_block_end"
	EventToolUseStart   EventKind = "tool_use_start"
	EventToolUseEnd     EventKind = "tool_use_end"
	EventToolResult     EventKind = "tool_result"
	EventResult         EventKind = "result"

	// User-driven events arriving from the client.
	EventToolDecision EventKind = "tool_decision"

	// Hook-driven events.
	EventTaskStarted      EventKind = "task_started"
	EventTaskNotification EventKind = "task_notification"
	EventTaskUpdated      EventKind = "task_updated"
	EventHookStarted      EventKind = "hook_started"
	EventHookResponse     EventKind = "hook_response"

	// Lifecycle events the server itself emits.
	EventClaudeError    EventKind = "claude_error"
	EventClaudeRunEnded EventKind = "claude_run_ended"

	// Optimistic-UI reconciliation events broadcast after Apply.
	EventTurnStarted          EventKind = "turn_started"
	EventToolDecisionApplied  EventKind = "tool_decision_applied"

	// Catch-all for stream-json kinds we don't care about.
	EventUnknown EventKind = "unknown"
)

// Event is the input to SessionState.Apply. Timestamp is the
// server's Apply-time wall clock; reducers stamp this into state
// fields like Turn.FinishedAt or ToolCall.StartedAt verbatim. The
// same Event is broadcast to clients so their reducer reaches the
// same state.
type Event struct {
	Kind      EventKind `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload"`
}

// EventWire is the on-wire shape used for unmarshal. Payload arrives
// as raw JSON and is decoded against the concrete type chosen by
// Kind. This lets one channel carry every event variant without an
// interface{}-vs-struct gymnastics dance.
type eventWire struct {
	Kind      EventKind       `json:"kind"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// UnmarshalJSON decodes Payload into the concrete struct matching Kind.
// Unknown kinds keep the raw RawMessage in Payload so the caller can
// inspect it without losing data.
func (e *Event) UnmarshalJSON(data []byte) error {
	var w eventWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	e.Kind = w.Kind
	e.Timestamp = w.Timestamp
	var pl any
	switch w.Kind {
	case EventTextDelta:
		pl = &TextDeltaPayload{}
	case EventThinkingDelta:
		pl = &ThinkingDeltaPayload{}
	case EventToolUseStart:
		pl = &ToolUseStartPayload{}
	case EventToolUseEnd:
		pl = &ToolUseEndPayload{}
	case EventToolResult:
		pl = &ToolResultPayload{}
	case EventMessageDelta:
		pl = &MessageDeltaPayload{}
	case EventResult:
		pl = &ResultPayload{}
	case EventTaskStarted:
		pl = &TaskStartedPayload{}
	case EventTaskNotification:
		pl = &TaskNotificationPayload{}
	case EventTaskUpdated:
		pl = &TaskUpdatedPayload{}
	case EventHookStarted:
		pl = &HookStartedPayload{}
	case EventHookResponse:
		pl = &HookResponsePayload{}
	case EventToolDecision:
		pl = &ToolDecisionPayload{}
	case EventTurnStarted:
		pl = &TurnStartedPayload{}
	case EventToolDecisionApplied:
		pl = &ToolDecisionAppliedPayload{}
	case EventClaudeError:
		pl = &ClaudeErrorPayload{}
	case EventClaudeRunEnded:
		pl = &ClaudeRunEndedPayload{}
	case EventMessageStart, EventMessageStop, EventTextBlockEnd, EventUnknown:
		// No payload of interest.
		e.Payload = nil
		return nil
	default:
		return fmt.Errorf("claudestate: unknown event kind %q", w.Kind)
	}
	if len(w.Payload) > 0 {
		if err := json.Unmarshal(w.Payload, pl); err != nil {
			return fmt.Errorf("claudestate: payload decode (kind=%s): %w", w.Kind, err)
		}
	}
	e.Payload = pl
	return nil
}

// MarshalJSON wraps Payload in the eventWire shape so encoding stays
// symmetric with UnmarshalJSON.
func (e Event) MarshalJSON() ([]byte, error) {
	pl, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(eventWire{
		Kind: e.Kind, Timestamp: e.Timestamp, Payload: pl,
	})
}

// ---- payload struct definitions -----------------------------------

type TextDeltaPayload struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type ThinkingDeltaPayload struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type ToolUseStartPayload struct {
	Index     int    `json:"index"`
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
}

type ToolUseEndPayload struct {
	ToolUseID string `json:"toolUseId"`
	Input     any    `json:"input,omitempty"`
}

type ToolResultPayload struct {
	ToolUseID string `json:"toolUseId"`
	Content   string `json:"content"`
	IsError   bool   `json:"isError"`
}

type MessageDeltaPayload struct {
	Usage TokenUsage `json:"usage"`
}

type ResultPayload struct {
	IsError      bool    `json:"isError"`
	TotalCostUsd float64 `json:"totalCostUsd"`
	Result       string  `json:"result,omitempty"`
}

type TaskStartedPayload struct {
	TaskID      string `json:"taskId"`
	ToolUseID   string `json:"toolUseId"`
	Description string `json:"description"`
	TaskType    string `json:"taskType"`
}

type TaskNotificationPayload struct {
	TaskID    string `json:"taskId"`
	ToolUseID string `json:"toolUseId"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
}

type TaskUpdatedPayload struct {
	TaskID  string         `json:"taskId"`
	Patch   map[string]any `json:"patch"`
}

type HookStartedPayload struct {
	HookID    string `json:"hookId"`
	HookEvent string `json:"hookEvent"`
	HookName  string `json:"hookName,omitempty"`
}

type HookResponsePayload struct {
	HookID    string `json:"hookId"`
	HookEvent string `json:"hookEvent"`
	ExitCode  int    `json:"exitCode"`
	Outcome   string `json:"outcome,omitempty"`
}

type ToolDecisionPayload struct {
	ToolUseID string `json:"toolUseId"`
	Decision  string `json:"decision"` // "allow" | "deny"
	Reason    string `json:"reason,omitempty"`
}

type TurnStartedPayload struct {
	ClientNonce string `json:"clientNonce"`
	TurnID      string `json:"turnId"`
	Prompt      string `json:"prompt"`
}

type ToolDecisionAppliedPayload struct {
	ToolUseID string `json:"toolUseId"`
	Decision  string `json:"decision"`
}

type ClaudeErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ClaudeRunEndedPayload struct {
	Message string `json:"message,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -run TestEvent_JSONRoundTrip -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/events.go internal/claudestate/events_test.go
git commit -m "feat(claudestate): Event union with kind-tagged JSON encoding

Event is the input to SessionState.Apply and the on-wire shape WS
broadcasts. Custom Marshal/UnmarshalJSON dispatches Payload to the
concrete struct keyed off Kind, so one channel carries every variant
without losing type info on the wire.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 4: SessionState skeleton + View

**Files:**
- Create: `internal/claudestate/state.go`
- Create: `internal/claudestate/state_test.go`

- [ ] **Step 1: Write the failing test**

```go
package claudestate

import (
	"testing"
)

func TestSessionState_View_ReturnsCopy(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	// Seed one turn through the internal mutator (not Apply yet — that's
	// Task 5; we directly poke the state for this isolation test).
	s.mu.Lock()
	s.state.Turns = append(s.state.Turns, ClaudeTurn{ID: "u1", Prompt: "hi"})
	s.mu.Unlock()

	var captured ClaudeState
	s.View(func(st *ClaudeState) {
		captured = st.DeepCopy()
	})
	// Mutating the captured copy must not affect the session's state.
	captured.Turns[0].Prompt = "MUTATED"

	s.View(func(st *ClaudeState) {
		if st.Turns[0].Prompt != "hi" {
			t.Errorf("session state mutated through View: %q", st.Turns[0].Prompt)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestSessionState_View_ReturnsCopy -v
```

Expected: FAIL — `NewSessionState` / `View` undefined.

- [ ] **Step 3: Write `state.go` skeleton**

```go
package claudestate

import (
	"context"
	"sync"
)

// SessionState holds one Alfred session's in-memory Claude state plus
// its Persister. All mutations route through Apply (added in Task 5);
// callers wanting a read-only view call View under the read lock.
//
// Concurrency model: a single sync.RWMutex protects `state`. Apply
// takes the write lock for the whole reducer run; View takes the
// read lock for the duration of its callback. Callers SHOULD copy
// state inside View and do expensive work (JSON marshal, HTTP write)
// outside the lock.
type SessionState struct {
	sessionID  string
	claudeUUID string

	mu    sync.RWMutex
	state ClaudeState

	persister *Persister // nil until AttachPersister is called
}

// NewSessionState returns a fresh SessionState with an empty
// ClaudeState. The Persister is not attached — the SessionManager
// (Plan 2) wires it after construction so tests can run state logic
// without touching disk.
func NewSessionState(sessionID, claudeUUID string) *SessionState {
	return &SessionState{
		sessionID:  sessionID,
		claudeUUID: claudeUUID,
		state:      EmptyClaudeState(),
	}
}

// SessionID returns the Alfred session id this state belongs to.
func (s *SessionState) SessionID() string { return s.sessionID }

// ClaudeUUID returns the current Claude CLI session uuid (mutable
// after a /compact rotation; updated via SetClaudeUUID).
func (s *SessionState) ClaudeUUID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claudeUUID
}

// SetClaudeUUID updates the tracked Claude uuid. Called after the
// runner reports a rotation.
func (s *SessionState) SetClaudeUUID(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claudeUUID = uuid
}

// View runs fn against the state under the read lock. fn MUST NOT
// retain a reference to the *ClaudeState after returning; copy out
// what is needed and let the lock release.
func (s *SessionState) View(fn func(*ClaudeState)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.state)
}

// Close flushes the Persister (if attached) and stops its goroutine.
// Safe to call when persister is nil (test path).
func (s *SessionState) Close(ctx context.Context) error {
	if s.persister == nil {
		return nil
	}
	return s.persister.Close(ctx)
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -run TestSessionState_View_ReturnsCopy -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/state.go internal/claudestate/state_test.go
git commit -m "feat(claudestate): SessionState skeleton with RWMutex-guarded View

State container per Alfred session. Apply (the central mutator) lands
in the next commit; this commit lays the lock/View pattern so callers
have a stable read API to test against.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 5: Apply — text_delta + tool_use_start + tool_result reducers

**Files:**
- Modify: `internal/claudestate/state.go` (add Apply + reducer helpers)
- Modify: `internal/claudestate/state_test.go` (add Apply tests for these three kinds)

- [ ] **Step 1: Write the failing test**

Append to `state_test.go`:

```go
import (
	"time"
)

// Apply(text_delta) appends text to the per-turn block at the given
// index. Reuses existing text blocks for the same index.
func TestApply_TextDelta_Accumulates(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "hi", time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC))

	must(t, s.Apply(Event{
		Kind: EventTextDelta, Timestamp: tAt(7, 0, 1),
		Payload: &TextDeltaPayload{Index: 0, Text: "hel"},
	}))
	must(t, s.Apply(Event{
		Kind: EventTextDelta, Timestamp: tAt(7, 0, 2),
		Payload: &TextDeltaPayload{Index: 0, Text: "lo"},
	}))

	s.View(func(st *ClaudeState) {
		if got := blockText(st, 0, 0); got != "hello" {
			t.Errorf("text = %q want hello", got)
		}
	})
}

// Apply(tool_use_start) pushes a tool block at the array tail and
// records the server-stamped StartedAt.
func TestApply_ToolUseStart_StampsStartedAt(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "use a tool", tAt(7, 0, 0))
	must(t, s.Apply(Event{
		Kind: EventToolUseStart, Timestamp: tAt(7, 0, 5),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"},
	}))
	s.View(func(st *ClaudeState) {
		b := st.Turns[0].Blocks[0]
		if b.Kind != "tool" || b.Tool == nil {
			t.Fatalf("expected tool block, got %+v", b)
		}
		if b.Tool.StartedAt == nil || !b.Tool.StartedAt.Equal(tAt(7, 0, 5)) {
			t.Errorf("StartedAt = %v want %v", b.Tool.StartedAt, tAt(7, 0, 5))
		}
		if b.Tool.Decision != "pending" {
			t.Errorf("Decision = %q want pending", b.Tool.Decision)
		}
	})
}

// Apply(tool_result) sets Result/IsError on the matching tool block
// and stamps FinishedAt from the event timestamp.
func TestApply_ToolResult_PatchesByID(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "use a tool", tAt(7, 0, 0))
	must(t, s.Apply(Event{
		Kind: EventToolUseStart, Timestamp: tAt(7, 0, 5),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"},
	}))
	must(t, s.Apply(Event{
		Kind: EventToolResult, Timestamp: tAt(7, 0, 9),
		Payload: &ToolResultPayload{ToolUseID: "tu_1", Content: "ok", IsError: false},
	}))
	s.View(func(st *ClaudeState) {
		b := st.Turns[0].Blocks[0]
		if b.Tool.Result != "ok" {
			t.Errorf("Result = %q", b.Tool.Result)
		}
		if b.Tool.FinishedAt == nil || !b.Tool.FinishedAt.Equal(tAt(7, 0, 9)) {
			t.Errorf("FinishedAt = %v", b.Tool.FinishedAt)
		}
	})
}

// ---- helpers ----

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func tAt(h, m, sec int) time.Time {
	return time.Date(2026, 6, 18, h, m, sec, 0, time.UTC)
}

func blockText(st *ClaudeState, turnIdx, blockIdx int) string {
	return st.Turns[turnIdx].Blocks[blockIdx].Text
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestApply -v
```

Expected: FAIL — `BeginTurn`, `Apply` undefined.

- [ ] **Step 3: Implement `BeginTurn` + `Apply` skeleton + three reducer cases**

Append to `state.go`:

```go
// BeginTurn appends a fresh turn to the conversation. Called from the
// claude_prompt entry point (Plan 2) to optimistically register the
// user's outgoing prompt before any stream events arrive.
func (s *SessionState) BeginTurn(id, prompt string, startedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Turns = append(s.state.Turns, ClaudeTurn{
		ID:        id,
		Prompt:    prompt,
		StartedAt: startedAt,
		Blocks:    []AssistantBlock{},
	})
	s.state.InFlight = true
}

// Apply folds one Event into the in-memory state under the write
// lock. Single mutation entry point. Returns an error only when the
// payload is wrong-typed for its kind — silently no-ops on benign
// missing data (e.g. tool_result for an unknown id) so caller logic
// stays linear.
func (s *SessionState) Apply(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch ev.Kind {
	case EventTextDelta:
		p, ok := ev.Payload.(*TextDeltaPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyTextDelta(p)
	case EventToolUseStart:
		p, ok := ev.Payload.(*ToolUseStartPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyToolUseStart(p, ev.Timestamp)
	case EventToolResult:
		p, ok := ev.Payload.(*ToolResultPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyToolResult(p, ev.Timestamp)
	default:
		// Remaining kinds wired in Task 6. Until then they're a no-op so
		// integration tests can submit them without crashing.
	}
	return nil
}

// applyTextDelta appends text to the block at `index` within the
// current turn. Holds the write lock; caller is responsible for
// acquiring it.
func (s *SessionState) applyTextDelta(p *TextDeltaPayload) {
	turn := s.lastTurn()
	if turn == nil || turn.Done {
		return
	}
	pos, ok := lookupBlockPos(turn, p.Index, "text")
	if !ok {
		turn.Blocks = append(turn.Blocks, AssistantBlock{Kind: "text"})
		pos = len(turn.Blocks) - 1
		ensureBlockIndex(turn, p.Index, pos)
	}
	turn.Blocks[pos].Text += p.Text
}

func (s *SessionState) applyToolUseStart(p *ToolUseStartPayload, ts time.Time) {
	turn := s.lastTurn()
	if turn == nil || turn.Done || p.ToolUseID == "" {
		return
	}
	turn.Blocks = append(turn.Blocks, AssistantBlock{
		Kind: "tool",
		Tool: &ClaudeToolCall{
			ToolUseID: p.ToolUseID,
			Name:      p.Name,
			Decision:  "pending",
			StartedAt: timePtr(ts),
		},
	})
	ensureBlockIndex(turn, p.Index, len(turn.Blocks)-1)
}

func (s *SessionState) applyToolResult(p *ToolResultPayload, ts time.Time) {
	turn := s.lastTurn()
	if turn == nil || p.ToolUseID == "" {
		return
	}
	for i := range turn.Blocks {
		b := &turn.Blocks[i]
		if b.Kind == "tool" && b.Tool != nil && b.Tool.ToolUseID == p.ToolUseID {
			b.Tool.Result = p.Content
			b.Tool.IsError = p.IsError
			b.Tool.FinishedAt = timePtr(ts)
			return
		}
	}
}

// ---- internal turn bookkeeping ----

// lastTurn returns a pointer to the current in-progress turn (or nil).
// Must be called under the write lock.
func (s *SessionState) lastTurn() *ClaudeTurn {
	n := len(s.state.Turns)
	if n == 0 {
		return nil
	}
	return &s.state.Turns[n-1]
}

// blockIndexMap is per-turn, transient — it stays in the SessionState
// (keyed by turn id) because we don't want it riding along on ClaudeTurn
// (it shouldn't appear on the wire). For Task 5 we keep it inline as a
// map field on SessionState; Task 6 reasons about message_start resets.
//
// Position lookups read from the helper map; if the lookup misses, the
// caller creates a fresh block and records the position.

// ensureBlockIndex is a no-op stub at this task — Task 6 wires the
// real map after message_start handling. The current implementation
// scans Blocks for the most recent block matching the index, which is
// correct for single-message turns (all current callers).
func ensureBlockIndex(_ *ClaudeTurn, _ int, _ int) {
	// Filled in Task 6.
}

func lookupBlockPos(turn *ClaudeTurn, index int, want string) (int, bool) {
	// Single-message-turn shortcut: the kind+index pair currently
	// uniquely identifies a block; multi-message handling lands in
	// Task 6 with the index map reset on message_start.
	for i, b := range turn.Blocks {
		if b.Kind == want {
			// We don't have access to the index attribute yet, so this is
			// best-effort: match the FIRST block of the right kind at or
			// after position `index`. Good enough for the Task 5 fixtures.
			if i >= index {
				return i, true
			}
		}
	}
	return -1, false
}
```

Also add the `import "fmt"` and `import "time"` lines to `state.go`. The full imports block becomes:

```go
import (
	"context"
	"fmt"
	"sync"
	"time"
)
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -run TestApply -v
```

Expected: 3 PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/state.go internal/claudestate/state_test.go
git commit -m "feat(claudestate): Apply for text_delta + tool_use_start + tool_result

The three highest-frequency stream-json events. Apply is the single
mutation entry point — held under write lock. Reducers stamp server-
side timestamps from ev.Timestamp into the corresponding state fields,
removing the need for the frontend to call new Date().

Multi-message index resets land in the next commit (message_start
handler + per-turn index maps).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 6: Apply — message_start reset + per-turn index map

**Files:**
- Modify: `internal/claudestate/state.go`
- Modify: `internal/claudestate/state_test.go` (multi-message ordering test)

- [ ] **Step 1: Write the failing test**

Append to `state_test.go`:

```go
// Multi-message-turn block ordering regression. Anthropic stream-json
// resets content-block `index` to 0 on each message_start. A single
// Alfred turn often spans multiple assistant messages (text → tool_use
// → tool_result → next assistant message). The reducer must reset its
// per-turn index map on message_start so the next message's index=0
// opens a fresh block — otherwise text folds into the prior message's
// block and tools sink to the array tail.
func TestApply_MultiMessage_KeepsInterleavedOrder(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "do a thing", tAt(7, 0, 0))

	// Message 1
	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 1)}))
	must(t, s.Apply(Event{Kind: EventTextDelta, Timestamp: tAt(7, 0, 2),
		Payload: &TextDeltaPayload{Index: 0, Text: "first reply "}}))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 3),
		Payload: &ToolUseStartPayload{Index: 1, ToolUseID: "tu_1", Name: "Bash"}}))
	must(t, s.Apply(Event{Kind: EventToolResult, Timestamp: tAt(7, 0, 4),
		Payload: &ToolResultPayload{ToolUseID: "tu_1", Content: "ok"}}))
	// Message 2 — index counter resets server-side.
	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 5)}))
	must(t, s.Apply(Event{Kind: EventTextDelta, Timestamp: tAt(7, 0, 6),
		Payload: &TextDeltaPayload{Index: 0, Text: "second reply "}}))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 7),
		Payload: &ToolUseStartPayload{Index: 1, ToolUseID: "tu_2", Name: "Read"}}))

	s.View(func(st *ClaudeState) {
		got := blockSummary(st.Turns[0].Blocks)
		want := []string{
			"text:first reply ",
			"tool:tu_1",
			"text:second reply ",
			"tool:tu_2",
		}
		if !equalStrSlice(got, want) {
			t.Errorf("blocks order:\n got  %v\n want %v", got, want)
		}
	})
}

func TestApply_MultiMessage_KeepsThinkingBlocksSeparate(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "think hard", tAt(7, 0, 0))

	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 1)}))
	must(t, s.Apply(Event{Kind: EventThinkingDelta, Timestamp: tAt(7, 0, 2),
		Payload: &ThinkingDeltaPayload{Index: 0, Text: "thought A"}}))
	must(t, s.Apply(Event{Kind: EventMessageStart, Timestamp: tAt(7, 0, 3)}))
	must(t, s.Apply(Event{Kind: EventThinkingDelta, Timestamp: tAt(7, 0, 4),
		Payload: &ThinkingDeltaPayload{Index: 0, Text: "thought B"}}))

	s.View(func(st *ClaudeState) {
		want := []string{"thought A", "thought B"}
		if !equalStrSlice(st.Turns[0].Thinking, want) {
			t.Errorf("thinking: got %v want %v", st.Turns[0].Thinking, want)
		}
	})
}

func blockSummary(blocks []AssistantBlock) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		if b.Kind == "tool" {
			out[i] = "tool:" + b.Tool.ToolUseID
		} else {
			out[i] = "text:" + b.Text
		}
	}
	return out
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestApply_MultiMessage -v
```

Expected: FAIL — second message's text folds into the first turn's first block, second tool lands at index 2 not 3.

- [ ] **Step 3: Replace the index-map stubs with real per-turn maps + thinking_delta + message_start handlers**

In `state.go`, replace the `// blockIndexMap is per-turn...` comment block and the two stub functions `lookupBlockPos` and `ensureBlockIndex` with:

```go
// perTurnIndex tracks the per-message content-block index → array
// position mapping for the current in-progress turn. Reset on each
// EventMessageStart. The map is keyed by Apply-time turn ID so a
// turn spanning many messages keeps cumulative blocks but each
// message's index space is fresh.
type perTurnIndex struct {
	turnID    string
	blocks    map[int]int // content-block index → position in Turn.Blocks
	thinking  map[int]int // content-block index → position in Turn.Thinking
}

// indexFor returns (and lazily resets) the index map for the active
// turn. Called under write lock.
func (s *SessionState) indexFor(turn *ClaudeTurn) *perTurnIndex {
	if s.curIndex == nil || s.curIndex.turnID != turn.ID {
		s.curIndex = &perTurnIndex{
			turnID:   turn.ID,
			blocks:   map[int]int{},
			thinking: map[int]int{},
		}
	}
	return s.curIndex
}

// resetBlockIndex empties the per-turn maps. Called on message_start
// so the next message's index=0 maps to a fresh block position.
func (s *SessionState) resetBlockIndex() {
	if s.curIndex != nil {
		s.curIndex.blocks = map[int]int{}
		s.curIndex.thinking = map[int]int{}
	}
}
```

Add a `curIndex *perTurnIndex` field to `SessionState`:

```go
type SessionState struct {
	sessionID  string
	claudeUUID string

	mu       sync.RWMutex
	state    ClaudeState
	curIndex *perTurnIndex // transient; not serialized

	persister *Persister
}
```

Rewrite `applyTextDelta`, `applyToolUseStart`, and add `applyThinkingDelta` + `applyMessageStart`:

```go
func (s *SessionState) applyTextDelta(p *TextDeltaPayload) {
	turn := s.lastTurn()
	if turn == nil || turn.Done {
		return
	}
	idx := s.indexFor(turn)
	pos, ok := idx.blocks[p.Index]
	if !ok || turn.Blocks[pos].Kind != "text" {
		turn.Blocks = append(turn.Blocks, AssistantBlock{Kind: "text"})
		pos = len(turn.Blocks) - 1
		idx.blocks[p.Index] = pos
	}
	turn.Blocks[pos].Text += p.Text
}

func (s *SessionState) applyToolUseStart(p *ToolUseStartPayload, ts time.Time) {
	turn := s.lastTurn()
	if turn == nil || turn.Done || p.ToolUseID == "" {
		return
	}
	idx := s.indexFor(turn)
	turn.Blocks = append(turn.Blocks, AssistantBlock{
		Kind: "tool",
		Tool: &ClaudeToolCall{
			ToolUseID: p.ToolUseID,
			Name:      p.Name,
			Decision:  "pending",
			StartedAt: timePtr(ts),
		},
	})
	idx.blocks[p.Index] = len(turn.Blocks) - 1
}

func (s *SessionState) applyThinkingDelta(p *ThinkingDeltaPayload) {
	turn := s.lastTurn()
	if turn == nil || turn.Done {
		return
	}
	idx := s.indexFor(turn)
	pos, ok := idx.thinking[p.Index]
	if !ok {
		turn.Thinking = append(turn.Thinking, "")
		pos = len(turn.Thinking) - 1
		idx.thinking[p.Index] = pos
	}
	turn.Thinking[pos] += p.Text
}

func (s *SessionState) applyMessageStart() {
	turn := s.lastTurn()
	if turn == nil || turn.Done {
		return
	}
	// Make sure the per-turn map exists (so the reset has something to
	// reset) then wipe it. Without this, the next message's index=0
	// folds back into the previous message's blocks.
	_ = s.indexFor(turn)
	s.resetBlockIndex()
}
```

Wire the new cases into `Apply`:

```go
func (s *SessionState) Apply(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch ev.Kind {
	case EventMessageStart:
		s.applyMessageStart()
	case EventTextDelta:
		p, ok := ev.Payload.(*TextDeltaPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyTextDelta(p)
	case EventThinkingDelta:
		p, ok := ev.Payload.(*ThinkingDeltaPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyThinkingDelta(p)
	case EventToolUseStart:
		p, ok := ev.Payload.(*ToolUseStartPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyToolUseStart(p, ev.Timestamp)
	case EventToolResult:
		p, ok := ev.Payload.(*ToolResultPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyToolResult(p, ev.Timestamp)
	default:
		// Remaining kinds wired in Task 7.
	}
	return nil
}
```

Remove the now-dead `lookupBlockPos` and `ensureBlockIndex` functions.

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -v
```

Expected: all PASS, including both multi-message tests.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/state.go internal/claudestate/state_test.go
git commit -m "feat(claudestate): per-turn index map + message_start reset

Stream-json resets content-block index to 0 on each assistant
message_start. The reducer now keeps the index map keyed by the
current turn ID and wipes it on message_start so cross-message
text/tool interleaving renders in the order Claude streamed it.

Also adds thinking_delta handling using a parallel thinking-index map.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 7: Apply — tool_use_end, message_delta, result, finalize backstop

**Files:**
- Modify: `internal/claudestate/state.go`
- Modify: `internal/claudestate/state_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestApply_ToolUseEnd_PatchesInput(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 1),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"}}))
	must(t, s.Apply(Event{Kind: EventToolUseEnd, Timestamp: tAt(7, 0, 2),
		Payload: &ToolUseEndPayload{ToolUseID: "tu_1", Input: map[string]any{"command": "ls"}}}))
	s.View(func(st *ClaudeState) {
		in, _ := st.Turns[0].Blocks[0].Tool.Input.(map[string]any)
		if in["command"] != "ls" {
			t.Errorf("input: %+v", st.Turns[0].Blocks[0].Tool.Input)
		}
	})
}

func TestApply_MessageDelta_StoresUsage(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventMessageDelta, Timestamp: tAt(7, 0, 1),
		Payload: &MessageDeltaPayload{Usage: TokenUsage{InputTokens: 100, OutputTokens: 50}}}))
	s.View(func(st *ClaudeState) {
		if st.Turns[0].Usage == nil || st.Turns[0].Usage.InputTokens != 100 {
			t.Errorf("usage: %+v", st.Turns[0].Usage)
		}
	})
}

func TestApply_Result_FinalizesTurn(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventTextDelta, Timestamp: tAt(7, 0, 1),
		Payload: &TextDeltaPayload{Index: 0, Text: "hi"}}))
	must(t, s.Apply(Event{Kind: EventResult, Timestamp: tAt(7, 0, 5),
		Payload: &ResultPayload{IsError: false, TotalCostUsd: 0.001}}))
	s.View(func(st *ClaudeState) {
		if !st.Turns[0].Done {
			t.Error("not Done")
		}
		if st.Turns[0].FinishedAt == nil || !st.Turns[0].FinishedAt.Equal(tAt(7, 0, 5)) {
			t.Errorf("FinishedAt = %v", st.Turns[0].FinishedAt)
		}
		if st.Turns[0].TotalCostUsd == nil || *st.Turns[0].TotalCostUsd != 0.001 {
			t.Errorf("cost = %v", st.Turns[0].TotalCostUsd)
		}
		if st.InFlight {
			t.Error("InFlight still true")
		}
	})
}

func TestApply_ClaudeRunEnded_FinalizesInFlightAsError(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventClaudeRunEnded, Timestamp: tAt(7, 0, 9),
		Payload: &ClaudeRunEndedPayload{Message: "killed"}}))
	s.View(func(st *ClaudeState) {
		tt := st.Turns[0]
		if !tt.Done || !tt.IsError {
			t.Errorf("Done=%v IsError=%v", tt.Done, tt.IsError)
		}
		if tt.FinishedAt == nil || !tt.FinishedAt.Equal(tAt(7, 0, 9)) {
			t.Errorf("FinishedAt = %v", tt.FinishedAt)
		}
		// Synthetic text block surfaces the kill reason so the UI is
		// not blank when the runner died before any text arrived.
		if len(tt.Blocks) != 1 || tt.Blocks[0].Kind != "text" || tt.Blocks[0].Text != "killed" {
			t.Errorf("synthetic message: %+v", tt.Blocks)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -v
```

Expected: 4 new FAIL — Apply ignores `tool_use_end`, `message_delta`, `result`, `claude_run_ended`.

- [ ] **Step 3: Implement the new reducer cases**

Add to `state.go`:

```go
func (s *SessionState) applyToolUseEnd(p *ToolUseEndPayload) {
	turn := s.lastTurn()
	if turn == nil || p.ToolUseID == "" {
		return
	}
	for i := range turn.Blocks {
		b := &turn.Blocks[i]
		if b.Kind == "tool" && b.Tool != nil && b.Tool.ToolUseID == p.ToolUseID {
			b.Tool.Input = p.Input
			return
		}
	}
}

func (s *SessionState) applyMessageDelta(p *MessageDeltaPayload) {
	turn := s.lastTurn()
	if turn == nil {
		return
	}
	u := p.Usage
	turn.Usage = &u
}

func (s *SessionState) applyResult(p *ResultPayload, ts time.Time) {
	turn := s.lastTurn()
	s.state.InFlight = false
	if turn == nil {
		return
	}
	turn.Done = true
	turn.IsError = p.IsError
	turn.FinishedAt = timePtr(ts)
	if p.TotalCostUsd != 0 {
		c := p.TotalCostUsd
		turn.TotalCostUsd = &c
	}
	if len(turn.Blocks) == 0 && p.Result != "" {
		turn.Blocks = []AssistantBlock{{Kind: "text", Text: p.Result}}
	}
}

// finalizeInFlight closes off an unfinished trailing turn as an
// error. Called by claude_run_ended and claude_error so the composer
// unlocks even when no result event arrived.
func (s *SessionState) finalizeInFlight(reason string, ts time.Time) {
	turn := s.lastTurn()
	s.state.InFlight = false
	s.state.Pending = nil
	s.state.PendingQuestions = nil
	if turn == nil || turn.Done {
		return
	}
	turn.Done = true
	turn.IsError = true
	turn.FinishedAt = timePtr(ts)
	if len(turn.Blocks) == 0 && reason != "" {
		turn.Blocks = []AssistantBlock{{Kind: "text", Text: reason}}
	}
}
```

Wire the cases:

```go
	case EventToolUseEnd:
		p, ok := ev.Payload.(*ToolUseEndPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyToolUseEnd(p)
	case EventMessageDelta:
		p, ok := ev.Payload.(*MessageDeltaPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyMessageDelta(p)
	case EventResult:
		p, ok := ev.Payload.(*ResultPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyResult(p, ev.Timestamp)
	case EventClaudeRunEnded:
		p, ok := ev.Payload.(*ClaudeRunEndedPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.finalizeInFlight(p.Message, ev.Timestamp)
	case EventClaudeError:
		p, ok := ev.Payload.(*ClaudeErrorPayload)
		if !ok {
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.state.LastError = &ClaudeError{Code: p.Code, Message: p.Message}
		s.finalizeInFlight(p.Message, ev.Timestamp)
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/state.go internal/claudestate/state_test.go
git commit -m "feat(claudestate): tool_use_end + message_delta + result + run_ended

Completes the per-turn lifecycle: input is patched onto the tool block
on tool_use_end; usage accumulates on message_delta; result marks the
turn Done and stamps FinishedAt + cost. claude_run_ended and
claude_error both route through finalizeInFlight so a runner crash
still closes the turn (Done=true, IsError=true, FinishedAt=now) and
unlocks the composer.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 8: Apply — task / hook lifecycle + tool_decision

**Files:**
- Modify: `internal/claudestate/state.go`
- Modify: `internal/claudestate/state_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestApply_TaskStarted_LinksToolBlockAndCreatesBgTask(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "monitor", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 1),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_mon", Name: "Monitor"}}))
	must(t, s.Apply(Event{Kind: EventTaskStarted, Timestamp: tAt(7, 0, 2),
		Payload: &TaskStartedPayload{
			TaskID: "task_x", ToolUseID: "tu_mon",
			Description: "tail logs", TaskType: "local_bash",
		}}))
	s.View(func(st *ClaudeState) {
		bt, ok := st.BgTasks["task_x"]
		if !ok {
			t.Fatal("bgTask not created")
		}
		if bt.Status != "in_progress" {
			t.Errorf("status = %q", bt.Status)
		}
		if !bt.StartedAt.Equal(tAt(7, 0, 2)) {
			t.Errorf("StartedAt = %v", bt.StartedAt)
		}
		// The tool block now points at the bgTask.
		tool := st.Turns[0].Blocks[0].Tool
		if tool.BgTaskID != "task_x" {
			t.Errorf("BgTaskID = %q", tool.BgTaskID)
		}
	})
}

func TestApply_HookSubagent_FIFOPair(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventHookStarted, Timestamp: tAt(7, 0, 1),
		Payload: &HookStartedPayload{HookID: "h_start_1", HookEvent: "SubagentStart"}}))
	must(t, s.Apply(Event{Kind: EventHookStarted, Timestamp: tAt(7, 0, 2),
		Payload: &HookStartedPayload{HookID: "h_start_2", HookEvent: "SubagentStart"}}))
	must(t, s.Apply(Event{Kind: EventHookResponse, Timestamp: tAt(7, 0, 5),
		Payload: &HookResponsePayload{HookID: "h_stop_X", HookEvent: "SubagentStop"}}))
	s.View(func(st *ClaudeState) {
		// Oldest in-progress subagent (h_start_1) should be marked finished.
		first := st.Subagents["h_start_1"]
		second := st.Subagents["h_start_2"]
		if first.FinishedAt == nil {
			t.Error("oldest subagent should be finished")
		}
		if second.FinishedAt != nil {
			t.Error("newer subagent should still be in progress")
		}
	})
}

func TestApply_ToolDecision_PatchesBlockAndDropsApproval(t *testing.T) {
	s := NewSessionState("sess1", "uuid-1")
	s.BeginTurn("u1", "go", tAt(7, 0, 0))
	must(t, s.Apply(Event{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 1),
		Payload: &ToolUseStartPayload{Index: 0, ToolUseID: "tu_1", Name: "Bash"}}))
	// Seed a pending approval the way the server's tool_approval_request
	// handler will: append to the queue, then resolve via tool_decision.
	s.mu.Lock()
	s.state.Pending = append(s.state.Pending, ClaudeToolApproval{ToolUseID: "tu_1", Tool: "Bash"})
	s.mu.Unlock()

	must(t, s.Apply(Event{Kind: EventToolDecision, Timestamp: tAt(7, 0, 2),
		Payload: &ToolDecisionPayload{ToolUseID: "tu_1", Decision: "deny"}}))

	s.View(func(st *ClaudeState) {
		if len(st.Pending) != 0 {
			t.Errorf("pending not drained: %+v", st.Pending)
		}
		if st.Turns[0].Blocks[0].Tool.Decision != "deny" {
			t.Errorf("decision = %q", st.Turns[0].Blocks[0].Tool.Decision)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -v
```

Expected: 3 new FAIL.

- [ ] **Step 3: Implement task/hook/decision reducers**

Add to `state.go`:

```go
func (s *SessionState) applyTaskStarted(p *TaskStartedPayload, ts time.Time) {
	if p.TaskID == "" {
		return
	}
	s.state.BgTasks[p.TaskID] = BgTask{
		TaskID:      p.TaskID,
		ToolUseID:   p.ToolUseID,
		Description: p.Description,
		TaskType:    p.TaskType,
		StartedAt:   ts, // BgTask.StartedAt stays non-optional — task only exists once it started
		Status:      "in_progress",
	}
	// Link the matching tool block.
	for ti := range s.state.Turns {
		for bi := range s.state.Turns[ti].Blocks {
			b := &s.state.Turns[ti].Blocks[bi]
			if b.Kind == "tool" && b.Tool != nil && b.Tool.ToolUseID == p.ToolUseID {
				b.Tool.BgTaskID = p.TaskID
			}
		}
	}
}

func (s *SessionState) applyTaskNotification(p *TaskNotificationPayload, ts time.Time) {
	bt, ok := s.state.BgTasks[p.TaskID]
	if !ok {
		return
	}
	bt.NotificationCount++
	bt.LastEventSummary = p.Summary
	if p.Status == "completed" {
		bt.Status = "completed"
		if bt.FinishedAt == nil {
			bt.FinishedAt = timePtr(ts)
		}
	}
	s.state.BgTasks[p.TaskID] = bt
}

func (s *SessionState) applyTaskUpdated(p *TaskUpdatedPayload, ts time.Time) {
	bt, ok := s.state.BgTasks[p.TaskID]
	if !ok {
		return
	}
	if status, _ := p.Patch["status"].(string); status == "completed" || status == "failed" {
		bt.Status = status
		if et, ok := p.Patch["end_time"].(float64); ok && et > 0 {
			bt.FinishedAt = timePtr(time.Unix(0, int64(et)*int64(time.Millisecond)).UTC())
		} else {
			bt.FinishedAt = timePtr(ts)
		}
		s.state.BgTasks[p.TaskID] = bt
	}
}

func (s *SessionState) applyHookStarted(p *HookStartedPayload, ts time.Time) {
	if p.HookEvent != "SubagentStart" || p.HookID == "" {
		return
	}
	s.state.Subagents[p.HookID] = SubagentEntry{
		HookID:    p.HookID,
		StartedAt: ts,
	}
}

func (s *SessionState) applyHookResponse(p *HookResponsePayload, ts time.Time) {
	if p.HookEvent != "SubagentStop" {
		return
	}
	// FIFO pair: stamp FinishedAt on the oldest in-progress subagent.
	var oldestKey string
	var oldestTS time.Time
	for k, v := range s.state.Subagents {
		if v.FinishedAt != nil {
			continue
		}
		if oldestKey == "" || v.StartedAt.Before(oldestTS) {
			oldestKey = k
			oldestTS = v.StartedAt
		}
	}
	if oldestKey == "" {
		return
	}
	se := s.state.Subagents[oldestKey]
	se.FinishedAt = timePtr(ts)
	s.state.Subagents[oldestKey] = se
}

func (s *SessionState) applyToolDecision(p *ToolDecisionPayload) {
	// Drop from pending queue.
	pending := s.state.Pending[:0]
	for _, q := range s.state.Pending {
		if q.ToolUseID != p.ToolUseID {
			pending = append(pending, q)
		}
	}
	s.state.Pending = pending
	// Mark the tool block.
	for ti := range s.state.Turns {
		for bi := range s.state.Turns[ti].Blocks {
			b := &s.state.Turns[ti].Blocks[bi]
			if b.Kind == "tool" && b.Tool != nil && b.Tool.ToolUseID == p.ToolUseID {
				b.Tool.Decision = p.Decision
			}
		}
	}
}
```

Wire the cases in `Apply`:

```go
	case EventTaskStarted:
		p, _ := ev.Payload.(*TaskStartedPayload)
		if p != nil {
			s.applyTaskStarted(p, ev.Timestamp)
		}
	case EventTaskNotification:
		p, _ := ev.Payload.(*TaskNotificationPayload)
		if p != nil {
			s.applyTaskNotification(p, ev.Timestamp)
		}
	case EventTaskUpdated:
		p, _ := ev.Payload.(*TaskUpdatedPayload)
		if p != nil {
			s.applyTaskUpdated(p, ev.Timestamp)
		}
	case EventHookStarted:
		p, _ := ev.Payload.(*HookStartedPayload)
		if p != nil {
			s.applyHookStarted(p, ev.Timestamp)
		}
	case EventHookResponse:
		p, _ := ev.Payload.(*HookResponsePayload)
		if p != nil {
			s.applyHookResponse(p, ev.Timestamp)
		}
	case EventToolDecision:
		p, _ := ev.Payload.(*ToolDecisionPayload)
		if p != nil {
			s.applyToolDecision(p)
		}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/state.go internal/claudestate/state_test.go
git commit -m "feat(claudestate): bg task + subagent + tool_decision reducers

Covers Monitor's task_started/notification/updated lifecycle, subagent
FIFO pairing on hook_started/hook_response, and the user's
tool_decision (allow/deny) draining from the pending queue and
patching the tool block.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 9: Persister — atomic write + dirty debounce

**Files:**
- Create: `internal/claudestate/persister.go`
- Create: `internal/claudestate/persister_test.go`

- [ ] **Step 1: Write the failing test**

```go
package claudestate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersister_DirtyDebounce_WritesOnce(t *testing.T) {
	dir := t.TempDir()
	st := NewSessionState("sess1", "uuid-1")
	st.BeginTurn("u1", "hi", tAt(7, 0, 0))

	p, err := NewPersister(filepath.Join(dir, "claude.json"), st, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	defer p.Close(context.Background())

	// Burst of dirties — debounce should collapse them.
	for i := 0; i < 10; i++ {
		p.MarkDirty()
	}
	// Wait long enough that debounce timer fired (30ms) plus margin.
	time.Sleep(100 * time.Millisecond)

	// Verify exactly one file exists and parses back.
	data, err := os.ReadFile(filepath.Join(dir, "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot parse: %v\n%s", err, data)
	}
	if snap.Version != 1 {
		t.Errorf("version: %d", snap.Version)
	}
	if snap.SessionID != "sess1" {
		t.Errorf("sessionId: %q", snap.SessionID)
	}
	if len(snap.Turns) != 1 || snap.Turns[0].ID != "u1" {
		t.Errorf("turns: %+v", snap.Turns)
	}
}

func TestPersister_AtomicWrite_NoOrphanTmp(t *testing.T) {
	dir := t.TempDir()
	st := NewSessionState("sess1", "uuid-1")
	p, err := NewPersister(filepath.Join(dir, "claude.json"), st, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	defer p.Close(context.Background())

	p.MarkDirty()
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("orphan tmp file left behind: %q", e.Name())
		}
	}
}

func TestPersister_Flush_Sync(t *testing.T) {
	dir := t.TempDir()
	st := NewSessionState("sess1", "uuid-1")
	st.BeginTurn("u1", "before flush", tAt(7, 0, 0))
	p, _ := NewPersister(filepath.Join(dir, "claude.json"), st, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	defer p.Close(context.Background())

	// Long debounce — without Flush we'd wait 30s.
	p.MarkDirty()
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// File must exist immediately.
	if _, err := os.Stat(filepath.Join(dir, "claude.json")); err != nil {
		t.Errorf("file not written after Flush: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestPersister -v
```

Expected: build failure — Persister / `snapshotFile` undefined.

- [ ] **Step 3: Write `persister.go`**

```go
package claudestate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// snapshotFile is the on-disk envelope for claude.json. The schema
// field set follows the spec: version + identity (sessionId,
// claudeUuid) + audit (writtenAt) + the persistable turns slice.
// Everything else in ClaudeState is recomputed on load.
type snapshotFile struct {
	Version    int          `json:"version"`
	SessionID  string       `json:"sessionId"`
	ClaudeUUID string       `json:"claudeUuid"`
	WrittenAt  time.Time    `json:"writtenAt"`
	Turns      []ClaudeTurn `json:"turns"`
}

const snapshotVersion = 1

// Persister owns one snapshot.json file on behalf of a SessionState.
// Run as a single goroutine: MarkDirty signals a write should happen
// after the debounce window; Flush forces an immediate write and
// blocks until it lands. Holding a file-level flock prevents two
// server processes from racing on the same snapshot.
type Persister struct {
	path     string
	tmpPath  string
	state    *SessionState
	debounce time.Duration

	dirty    chan struct{}        // cap 1 — coalesces signals
	flushReq chan chan error      // synchronous flush
	closeReq chan chan error      // synchronous close

	flockFile *os.File
	stopped   sync.Once
}

// NewPersister allocates the goroutine resources and acquires the
// flock. Caller MUST call Run to start servicing signals.
func NewPersister(path string, state *SessionState, debounce time.Duration) (*Persister, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("claudestate: mkdir for snapshot: %w", err)
	}
	lockPath := path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("claudestate: open lockfile: %w", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		return nil, fmt.Errorf("claudestate: snapshot held by another process: %w", err)
	}
	// Best-effort orphan tmp cleanup from a previous crash.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		_ = os.Remove(path + ".tmp")
	}
	return &Persister{
		path:      path,
		tmpPath:   path + ".tmp",
		state:     state,
		debounce:  debounce,
		dirty:     make(chan struct{}, 1),
		flushReq:  make(chan chan error),
		closeReq:  make(chan chan error),
		flockFile: lf,
	}, nil
}

// Run is the goroutine main loop. Exits when Close is called or ctx
// is canceled.
func (p *Persister) Run(ctx context.Context) {
	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}
	defer stopTimer()
	for {
		select {
		case <-p.dirty:
			stopTimer()
			timer = time.NewTimer(p.debounce)
		case <-timerC(timer):
			timer = nil
			if err := p.writeSnapshot(); err != nil {
				slog.Error("claudestate: snapshot write failed",
					"sessionID", p.state.SessionID(), "err", err)
			}
		case ack := <-p.flushReq:
			stopTimer()
			ack <- p.writeSnapshot()
		case ack := <-p.closeReq:
			stopTimer()
			err := p.writeSnapshot()
			p.releaseLock()
			ack <- err
			return
		case <-ctx.Done():
			stopTimer()
			p.releaseLock()
			return
		}
	}
}

// MarkDirty signals the goroutine that state changed. Non-blocking:
// if a signal is already queued the second one collapses into it.
func (p *Persister) MarkDirty() {
	select {
	case p.dirty <- struct{}{}:
	default:
	}
}

// Flush blocks until the next snapshot write completes (or fails).
func (p *Persister) Flush(ctx context.Context) error {
	ack := make(chan error, 1)
	select {
	case p.flushReq <- ack:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close synchronously flushes one final time and shuts down the
// goroutine. Idempotent.
func (p *Persister) Close(ctx context.Context) error {
	var finalErr error
	p.stopped.Do(func() {
		ack := make(chan error, 1)
		select {
		case p.closeReq <- ack:
			select {
			case finalErr = <-ack:
			case <-ctx.Done():
				finalErr = ctx.Err()
			}
		case <-ctx.Done():
			finalErr = ctx.Err()
		}
	})
	return finalErr
}

// writeSnapshot performs the standard tmp → fsync → rename atomic
// write. Hand-rolled because os.WriteFile skips Sync.
func (p *Persister) writeSnapshot() error {
	// Capture state under read lock.
	var snap snapshotFile
	p.state.View(func(st *ClaudeState) {
		copied := st.DeepCopy()
		snap = snapshotFile{
			Version:    snapshotVersion,
			SessionID:  p.state.SessionID(),
			ClaudeUUID: p.state.ClaudeUUID(),
			WrittenAt:  time.Now().UTC(),
			Turns:      copied.Turns,
		}
	})

	// Marshal outside the lock.
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	f, err := os.OpenFile(p.tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(p.tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(p.tmpPath)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p.tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(p.tmpPath, p.path); err != nil {
		os.Remove(p.tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	// fsync parent dir for crash durability.
	if d, err := os.Open(filepath.Dir(p.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (p *Persister) releaseLock() {
	if p.flockFile != nil {
		_ = syscall.Flock(int(p.flockFile.Fd()), syscall.LOCK_UN)
		_ = p.flockFile.Close()
		p.flockFile = nil
	}
}

// timerC returns the channel of a possibly-nil timer (nil channel
// blocks forever in a select, which is the behavior we want when
// nothing is scheduled).
func timerC(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}
```

Wire `SessionState` to its Persister:

In `state.go`, replace the existing `Close` method with:

```go
// AttachPersister installs p so subsequent Apply calls signal
// dirty. SessionManager (Plan 2) wires this immediately after
// NewSessionState. Test code can leave it unset to keep tests
// hermetic.
func (s *SessionState) AttachPersister(p *Persister) {
	s.persister = p
}

// Apply already takes the write lock; this hook fires the Persister
// dirty bit after every mutation. Cheap (single channel send) so we
// don't bother filtering on "did state actually change."
func (s *SessionState) markDirty() {
	if s.persister != nil {
		s.persister.MarkDirty()
	}
}
```

And add `s.markDirty()` as the last statement before each `return nil` in `Apply` — or simpler, call it once before `return nil`:

```go
func (s *SessionState) Apply(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.markDirty()
	// ... switch unchanged ...
	return nil
}
```

And likewise in `BeginTurn`:

```go
func (s *SessionState) BeginTurn(id, prompt string, startedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.markDirty()
	// ... unchanged ...
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -v
```

Expected: all PASS (including the 3 new Persister tests).

- [ ] **Step 5: Commit**

```
git add internal/claudestate/persister.go internal/claudestate/persister_test.go internal/claudestate/state.go
git commit -m "feat(claudestate): Persister with debounced atomic snapshot writes

Per-SessionState goroutine: MarkDirty signals a write; debounce
collapses bursts; writes go through tmp + fsync + rename + parent-dir
fsync for crash durability. Flock guards against two server processes
racing on the same snapshot. Apply calls markDirty after every
mutation so the goroutine knows when to fire.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 10: Loader — snapshot-only and missing-snapshot paths

**Files:**
- Create: `internal/claudestate/loader.go`
- Create: `internal/claudestate/loader_test.go`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestLoad -v
```

Expected: build failure — `Load` undefined.

- [ ] **Step 3: Write `loader.go` (snapshot path + missing fallback)**

```go
// Package-level Load function. Merge logic for the
// snapshot-AND-jsonl path lands in Task 11.
package claudestate

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"

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
```

Add `import "time"` at the top of `loader.go`.

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -v
```

Expected: all PASS. The four new TestLoad cases now succeed; mergeTurns stub favours snapshot when both are present, which is fine for now — Task 11 brings the real merge.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/loader.go internal/claudestate/loader_test.go
git commit -m "feat(claudestate): Loader skeleton with snapshot + fallback paths

Reads claude.json + transcript jsonl and produces a ClaudeState. This
commit covers the corners: missing snapshot, missing jsonl, both
missing, version mismatch, corrupted file. Real per-field merge for
the snapshot-AND-jsonl case lands next, behind a stub that prefers
the snapshot.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 11: Loader — merge rules

**Files:**
- Modify: `internal/claudestate/loader.go`
- Modify: `internal/claudestate/loader_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestMergeTurns -v
```

Expected: FAIL. The stub returns snapshot wholesale; new tests want skeleton-from-jsonl, fields-from-snapshot.

- [ ] **Step 3: Replace `mergeTurns` stub with full implementation**

Replace the stub at the bottom of `loader.go` with:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/loader.go internal/claudestate/loader_test.go
git commit -m "feat(claudestate): merge rules — jsonl skeleton + snapshot overrides

Per-field merge per the spec: jsonl wins for chat skeleton (prompt,
blocks order, text, tool name/input/result), snapshot wins for
extension fields (tool startedAt/finishedAt/decision/bgTaskId, turn
finishedAt/cost/usage/isError). Snapshot-only turns and snapshot-only
tools are dropped — jsonl is the ground truth for what existed.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 12: Refresh-parity integration test

**Files:**
- Create: `internal/claudestate/integration_test.go`

- [ ] **Step 1: Write the failing test**

```go
package claudestate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The whole refactor's central invariant: feeding the same event
// sequence through the live reducer and the load-from-disk path
// produces equivalent state.
func TestRefreshParity_GoldenPath(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "claude.json")
	jsonlPath := filepath.Join(dir, "transcript.jsonl")

	// Synthesize a minimal jsonl bracketing the events below. The
	// merger needs this to provide the skeleton; the Persister provides
	// the extension fields.
	must(t, os.WriteFile(jsonlPath, []byte(
		`{"type":"user","message":{"role":"user","content":"do a thing"},"uuid":"u1","timestamp":"2026-06-18T07:00:00.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello "},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok","is_error":false}]},"timestamp":"2026-06-18T07:00:03.000Z"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n",
	), 0o600))

	// Live path: feed events through Apply and let Persister write.
	live := NewSessionState("sess1", "uuid-1")
	live.BeginTurn("u1", "do a thing", tAt(7, 0, 0))
	persister, err := NewPersister(snapPath, live, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	live.AttachPersister(persister)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go persister.Run(ctx)

	for _, ev := range []Event{
		{Kind: EventMessageStart, Timestamp: tAt(7, 0, 1)},
		{Kind: EventTextDelta, Timestamp: tAt(7, 0, 1),
			Payload: &TextDeltaPayload{Index: 0, Text: "hello "}},
		{Kind: EventToolUseStart, Timestamp: tAt(7, 0, 2),
			Payload: &ToolUseStartPayload{Index: 1, ToolUseID: "tu_1", Name: "Bash"}},
		{Kind: EventToolUseEnd, Timestamp: tAt(7, 0, 2),
			Payload: &ToolUseEndPayload{ToolUseID: "tu_1", Input: map[string]any{"command": "ls"}}},
		{Kind: EventToolDecision, Timestamp: time.Date(2026, 6, 18, 7, 0, 2, 500_000_000, time.UTC),
			Payload: &ToolDecisionPayload{ToolUseID: "tu_1", Decision: "allow"}},
		{Kind: EventToolResult, Timestamp: tAt(7, 0, 3),
			Payload: &ToolResultPayload{ToolUseID: "tu_1", Content: "ok"}},
		{Kind: EventMessageStart, Timestamp: tAt(7, 0, 4)},
		{Kind: EventTextDelta, Timestamp: tAt(7, 0, 4),
			Payload: &TextDeltaPayload{Index: 0, Text: "done"}},
		{Kind: EventMessageDelta, Timestamp: tAt(7, 0, 5),
			Payload: &MessageDeltaPayload{Usage: TokenUsage{InputTokens: 100, OutputTokens: 5}}},
		{Kind: EventResult, Timestamp: tAt(7, 0, 5),
			Payload: &ResultPayload{IsError: false, TotalCostUsd: 0.001}},
	} {
		must(t, live.Apply(ev))
	}
	must(t, persister.Flush(ctx))

	var liveState ClaudeState
	live.View(func(st *ClaudeState) { liveState = st.DeepCopy() })

	// Refresh path: discard live in-memory state and load from disk.
	loaded, err := Load(snapPath, jsonlPath)
	if err != nil {
		t.Fatal(err)
	}

	// Equivalence: same turn count, ids, blocks, extension fields.
	if !reflect.DeepEqual(liveState.Turns, loaded.Turns) {
		t.Errorf("turn equivalence failed.\n live:   %+v\n loaded: %+v",
			liveState.Turns, loaded.Turns)
	}
	if liveState.InFlight != loaded.InFlight {
		t.Errorf("inFlight: live=%v loaded=%v", liveState.InFlight, loaded.InFlight)
	}
}

```

Sub-second-precision events use `time.Date(...)` inline (see the
`EventToolDecision` event in the fixture above) — we don't extend the
`tAt` helper because only one call needs it.

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/claudestate/... -run TestRefreshParity_GoldenPath -v
```

Expected: depending on prior tasks the live state and loaded state may differ on a few fields (StartedAt on tools, for example, because jsonl can't supply ts and snapshot's value needs to land). If anything diverges, the test surfaces exactly which field. Iterate by tightening the live-vs-loaded comparison until they match.

- [ ] **Step 3: Reconcile any field divergence**

Two common divergence sources:
1. **`Turn.StartedAt`** — live records from `BeginTurn` time; loaded reads from jsonl user-line ts. Both should land at `tAt(7,0,0)`. If they differ, ensure the synthesized jsonl uses the same ISO timestamp.
2. **`Tool.Input`** — live receives the `ToolUseEnd` payload; loaded decodes from jsonl. Ensure both encode `map[string]any{"command":"ls"}`.

Fix anything off by tweaking the test fixture or the merge — the failing diff guides the exact edit. No new production code should be needed in this step (the previous tasks cover the reducer surface).

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/claudestate/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```
git add internal/claudestate/integration_test.go
git commit -m "test(claudestate): refresh-parity integration test

Drives a representative event sequence through Apply + Persister
(live path), then discards in-memory state and reloads via Load
(refresh path). Asserts equivalence of turns, blocks, and extension
fields. This is the central invariant the whole refactor is built
around — any future change that breaks it surfaces here.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Task 13: Run full suite + final commit

- [ ] **Step 1: Run race detector across the whole package**

```
go test ./internal/claudestate/... -race -v
```

Expected: PASS, no race warnings.

- [ ] **Step 2: Run the entire repo's Go tests to confirm no regressions outside the new package**

```
go test ./... 2>&1 | tail -20
```

Expected: PASS for every package except `cmd/alfred-server` if a running server is occupying port 8090 (preexisting environment condition, not related to this refactor).

- [ ] **Step 3: Verify no orphan tmp files in the package's test temp dirs**

`go test` cleans them with `t.TempDir()`, so this is just visual confirmation:

```
ls /tmp | grep claudestate 2>/dev/null || echo "no leftover dirs"
```

Expected: `no leftover dirs`.

- [ ] **Step 4: Confirm spec coverage**

Open `docs/superpowers/specs/2026-06-18-claude-refresh-parity-design.md` and the new code side-by-side. Verify each row of the "Fields persisted on `ClaudeTurn`" and "Fields persisted on `ClaudeToolCall`" tables maps to a real field in `types.go`.

- [ ] **Step 5: Push the branch (no PR yet — Plan 2 keeps adding)**

```
git push -u origin refactor/refresh-parity
```

---

## Spec coverage check (self-review)

| Spec requirement | Plan 1 task |
| --- | --- |
| `internal/claudestate` package skeleton | Task 1 |
| Public types with camelCase JSON tags | Task 1 |
| `DeepCopy` helpers (no reflect / JSON round-trip) | Task 2 |
| `Event` tagged union with kind-dispatched Payload | Task 3 |
| `SessionState` with RWMutex + View | Task 4 |
| `Apply` reducer for stream-json + lifecycle events | Tasks 5, 6, 7 |
| Multi-message block ordering via per-turn index map | Task 6 |
| Task / hook / decision reducers | Task 8 |
| `Persister` debounced atomic write + flock | Task 9 |
| Orphan tmp cleanup | Task 9 (NewPersister) |
| `Loader` snapshot + jsonl entry point | Task 10 |
| Merge rules per Section 4 | Task 11 |
| `TestRefreshParity_GoldenPath` | Task 12 |
| Race-detector pass | Task 13 |

Out-of-scope (deferred to Plan 2 / Plan 3):
- `SessionManager` singleton + singleflight `GetOrLoad`
- HTTP `/claude-state` endpoint
- WS event ingestion routing through Apply
- Frontend reducer simplification
- Playwright refresh-parity test
- Deprecating `/claude-history`

These are tracked in the spec's Plan Hand-off section and will land in `2026-06-18-refresh-parity-02-http-ws.md` and `-03-frontend-cutover.md`.
