# Turn/Tool Outcome Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the two-boolean (`done`+`isError`) turn/tool end-state encoding with an explicit `outcome` enum that distinguishes completed / errored / aborted, so interrupts are no longer mislabeled as errors and the frontend stops lying with "Done".

**Architecture:** Add an additive `Outcome` (and tool `AbortReason` for turns) field as the source of truth; keep `Done`/`IsError` as derived quantities written by a single `setTurnOutcome`/`setToolOutcome` helper. Route all termination paths through the helper, emit one unified greppable interrupt log, backfill old snapshots on Load, and switch frontend `turnPhase`/`toolStatus` to read `outcome`.

**Tech Stack:** Go (backend state machine + loader), TypeScript/React (frontend rendering), Go `testing` + `vitest`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-06-20-turn-tool-outcome-semantics-design.md` (verbatim source of truth).
- Outcome enum values — turn: `"" | "completed" | "errored" | "aborted"`; tool: same plus `"denied"`. `""` means not terminated.
- `done = (outcome != "")`; `isError = (outcome == "errored" || outcome == "aborted")`. Never set `Done`/`IsError` directly in termination paths — go through the helper.
- Dividing line: Claude/CLI's own failure = `errored`; external interruption = `aborted`.
- No snapshot version bump (fields are additive + backfilled, `omitempty`).
- First terminator wins (preserve the existing `turn.Done` guard).
- AbortReason codes: `runner_killed` | `ws_disconnect` | `server_shutdown` | `server_restart` | `spawn_failed` | `rate_limit`.
- Unified log msg: `"claude turn terminated abnormally"` with fields `sessionId`, `turnId`, `outcome`, `reason`, `hangingTools`.
- TDD: red-before-green for every behavior. Frequent commits.
- Run Go tests with `go test ./internal/claudestate/` and frontend with `cd web && npx vitest run`.

## File Structure

- `internal/claudestate/types.go` — add `Outcome`/`AbortReason` to `ClaudeTurn`, `Outcome` to `ClaudeToolCall`.
- `internal/claudestate/outcome.go` (NEW) — `setTurnOutcome`, `setToolOutcome`, `backfillOutcome`, the outcome→done/isError derivation, and `logAbnormalTermination`.
- `internal/claudestate/outcome_test.go` (NEW) — unit tests for the helpers.
- `internal/claudestate/state.go` — route `applyResult`, `finalizeInFlight`, `applyToolResult` through the helpers; thread a reason into the run-ended/error paths.
- `internal/claudestate/loader.go` — `finalizeStaleTrailingTurn`, `finalizeHangingToolBlocks`, plus a `backfillOutcome` pass on every loaded turn.
- `web/src/features/sessions/types.ts` — mirror `outcome`/`abortReason` on the TS types.
- `web/src/features/sessions/ClaudeChatView.tsx` — `turnPhase()` and `toolStatus()` switch on `outcome`.
- `web/src/features/sessions/claudeReducer.ts` — set `outcome` in `finalizeInFlightTurn` and the `result` case.

---

### Task 1: Add outcome fields to the Go types

**Files:**
- Modify: `internal/claudestate/types.go:34-51` (ClaudeTurn), `:65-79` (ClaudeToolCall)

**Interfaces:**
- Produces: `ClaudeTurn.Outcome string`, `ClaudeTurn.AbortReason string`, `ClaudeToolCall.Outcome string` (all `json:"...,omitempty"`).

- [ ] **Step 1: Add fields to ClaudeTurn**

In `internal/claudestate/types.go`, inside `type ClaudeTurn struct`, after the `IsError` line (`:48`):

```go
	IsError      bool             `json:"isError,omitempty"`
	// Outcome is the terminal state and the source of truth; Done and
	// IsError are derived from it (see outcome.go). "" == in progress.
	Outcome      string           `json:"outcome,omitempty"` // "completed" | "errored" | "aborted"
	// AbortReason is a machine-readable code, non-empty only for
	// errored/aborted turns. Codes: runner_killed | ws_disconnect |
	// server_shutdown | server_restart | spawn_failed | rate_limit.
	AbortReason  string           `json:"abortReason,omitempty"`
```

- [ ] **Step 2: Add field to ClaudeToolCall**

In `type ClaudeToolCall struct`, after the `Decision` line (`:69`):

```go
	Decision  string `json:"decision"` // "allow" | "deny" | "pending"
	// Outcome is the tool's terminal state. "" == not terminated.
	// "completed" | "errored" | "aborted" | "denied".
	Outcome   string `json:"outcome,omitempty"`
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./internal/claudestate/`
Expected: builds clean (no test yet — pure field addition).

- [ ] **Step 4: Commit**

```bash
git add internal/claudestate/types.go
git commit -m "feat(claudestate): add Outcome/AbortReason fields to turn + tool types"
```

---

### Task 2: The outcome helpers (setTurnOutcome / setToolOutcome / derivation)

**Files:**
- Create: `internal/claudestate/outcome.go`
- Create: `internal/claudestate/outcome_test.go`

**Interfaces:**
- Consumes: `ClaudeTurn`, `ClaudeToolCall` from Task 1.
- Produces:
  - `func setTurnOutcome(t *ClaudeTurn, outcome, reason string, ts time.Time)` — first-terminator-wins; sets Outcome/AbortReason/Done/IsError/FinishedAt.
  - `func setToolOutcome(t *ClaudeToolCall, outcome string, ts time.Time)` — first-terminator-wins; sets Outcome/IsError/FinishedAt.
  - `func isErrorOutcome(outcome string) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/claudestate/outcome_test.go`:

```go
package claudestate

import (
	"testing"
	"time"
)

func tsAt(h, m int) time.Time { return time.Date(2026, 6, 20, h, m, 0, 0, time.UTC) }

func TestSetTurnOutcome_DerivesDoneIsError(t *testing.T) {
	cases := []struct {
		outcome       string
		wantDone      bool
		wantIsError   bool
	}{
		{"completed", true, false},
		{"errored", true, true},
		{"aborted", true, true},
	}
	for _, c := range cases {
		turn := &ClaudeTurn{ID: "u1"}
		setTurnOutcome(turn, c.outcome, "", tsAt(7, 0))
		if turn.Outcome != c.outcome {
			t.Errorf("%s: Outcome=%q", c.outcome, turn.Outcome)
		}
		if turn.Done != c.wantDone {
			t.Errorf("%s: Done=%v want %v", c.outcome, turn.Done, c.wantDone)
		}
		if turn.IsError != c.wantIsError {
			t.Errorf("%s: IsError=%v want %v", c.outcome, turn.IsError, c.wantIsError)
		}
		if turn.FinishedAt == nil {
			t.Errorf("%s: FinishedAt nil", c.outcome)
		}
	}
}

func TestSetTurnOutcome_FirstTerminatorWins(t *testing.T) {
	turn := &ClaudeTurn{ID: "u1"}
	setTurnOutcome(turn, "completed", "", tsAt(7, 0))
	setTurnOutcome(turn, "aborted", "runner_killed", tsAt(7, 5))
	if turn.Outcome != "completed" {
		t.Errorf("second terminator overwrote: Outcome=%q", turn.Outcome)
	}
	if turn.AbortReason != "" {
		t.Errorf("second terminator set AbortReason=%q", turn.AbortReason)
	}
}

func TestSetTurnOutcome_RecordsAbortReason(t *testing.T) {
	turn := &ClaudeTurn{ID: "u1"}
	setTurnOutcome(turn, "aborted", "ws_disconnect", tsAt(7, 0))
	if turn.AbortReason != "ws_disconnect" {
		t.Errorf("AbortReason=%q", turn.AbortReason)
	}
}

func TestSetToolOutcome_DerivesIsError(t *testing.T) {
	tool := &ClaudeToolCall{ToolUseID: "t1"}
	setToolOutcome(tool, "aborted", tsAt(7, 0))
	if tool.Outcome != "aborted" || !tool.IsError || tool.FinishedAt == nil {
		t.Errorf("got Outcome=%q IsError=%v FinishedAt=%v", tool.Outcome, tool.IsError, tool.FinishedAt)
	}
	// denied is a terminal state but NOT an error.
	tool2 := &ClaudeToolCall{ToolUseID: "t2"}
	setToolOutcome(tool2, "denied", tsAt(7, 0))
	if !tool2.IsError == false { // denied → isError stays false
	}
	if tool2.IsError {
		t.Errorf("denied should not be isError")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claudestate/ -run 'TestSetTurnOutcome|TestSetToolOutcome' -v`
Expected: FAIL — `undefined: setTurnOutcome` / `setToolOutcome`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/claudestate/outcome.go`:

```go
package claudestate

import (
	"log/slog"
	"time"
)

// isErrorOutcome reports whether an outcome should render as an error
// (red). Both Claude's own errors and external interrupts do.
func isErrorOutcome(outcome string) bool {
	return outcome == "errored" || outcome == "aborted"
}

// setTurnOutcome is the SOLE writer of a turn's terminal state.
// Done/IsError/FinishedAt are derived from outcome so the 12 termination
// paths can never disagree. First terminator wins (an already-terminated
// turn is left untouched), preserving the prior turn.Done guard.
func setTurnOutcome(t *ClaudeTurn, outcome, reason string, ts time.Time) {
	if t == nil || t.Outcome != "" {
		return
	}
	t.Outcome = outcome
	t.AbortReason = reason
	t.Done = true
	t.IsError = isErrorOutcome(outcome)
	t.FinishedAt = timePtr(ts)
}

// setToolOutcome is the sole writer of a tool block's terminal state.
// "denied" is a terminal state but not an error.
func setToolOutcome(t *ClaudeToolCall, outcome string, ts time.Time) {
	if t == nil || t.Outcome != "" {
		return
	}
	t.Outcome = outcome
	t.IsError = isErrorOutcome(outcome)
	if t.FinishedAt == nil {
		t.FinishedAt = timePtr(ts)
	}
}

// logAbnormalTermination emits the one greppable interrupt log used by
// every errored/aborted path. hangingTools is the count of tool blocks
// the same finalize closed (0 for the common case).
func logAbnormalTermination(sessionID, turnID, outcome, reason string, hangingTools int) {
	slog.Warn("claude turn terminated abnormally",
		"sessionId", sessionID,
		"turnId", turnID,
		"outcome", outcome,
		"reason", reason,
		"hangingTools", hangingTools)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claudestate/ -run 'TestSetTurnOutcome|TestSetToolOutcome' -v`
Expected: PASS (all 5 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/claudestate/outcome.go internal/claudestate/outcome_test.go
git commit -m "feat(claudestate): setTurnOutcome/setToolOutcome helpers + abnormal-termination log"
```

---

### Task 3: Route applyResult + finalizeInFlight through setTurnOutcome

**Files:**
- Modify: `internal/claudestate/state.go` (`applyResult` ~`:330`, `finalizeInFlight` `:360`, the two call sites `:179`,`:186`)
- Test: `internal/claudestate/outcome_test.go` (append)

**Interfaces:**
- Consumes: `setTurnOutcome` (Task 2), `logAbnormalTermination` (Task 2).
- Produces: `finalizeInFlight(reason, abortReason, outcome string, ts time.Time)` — new signature carrying the outcome + machine reason.

- [ ] **Step 1: Write the failing test**

Append to `internal/claudestate/outcome_test.go`:

```go
func TestApplyResult_SetsCompletedOutcome(t *testing.T) {
	s := newTestSessionState(t)
	s.state.Turns = []ClaudeTurn{{ID: "u1", StartedAt: tsAt(7, 0)}}
	s.state.InFlight = true
	s.applyResult(&ResultPayload{IsError: false, Result: "ok"}, tsAt(7, 1))
	if s.state.Turns[0].Outcome != "completed" {
		t.Errorf("Outcome=%q want completed", s.state.Turns[0].Outcome)
	}
}

func TestApplyResult_SetsErroredOutcome(t *testing.T) {
	s := newTestSessionState(t)
	s.state.Turns = []ClaudeTurn{{ID: "u1", StartedAt: tsAt(7, 0)}}
	s.state.InFlight = true
	s.applyResult(&ResultPayload{IsError: true, Result: "boom"}, tsAt(7, 1))
	if s.state.Turns[0].Outcome != "errored" {
		t.Errorf("Outcome=%q want errored", s.state.Turns[0].Outcome)
	}
}

func TestFinalizeInFlight_SetsAbortedOutcome(t *testing.T) {
	s := newTestSessionState(t)
	s.state.Turns = []ClaudeTurn{{ID: "u1", StartedAt: tsAt(7, 0)}}
	s.state.InFlight = true
	s.finalizeInFlight("client disconnected", "ws_disconnect", "aborted", tsAt(7, 1))
	turn := s.state.Turns[0]
	if turn.Outcome != "aborted" || turn.AbortReason != "ws_disconnect" {
		t.Errorf("Outcome=%q AbortReason=%q", turn.Outcome, turn.AbortReason)
	}
	if s.state.InFlight {
		t.Error("InFlight should be false")
	}
}
```

If `newTestSessionState` does not exist, add this helper to `outcome_test.go`:

```go
func newTestSessionState(t *testing.T) *SessionState {
	t.Helper()
	return &SessionState{state: EmptyClaudeState()}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claudestate/ -run 'TestApplyResult_Sets|TestFinalizeInFlight_SetsAborted' -v`
Expected: FAIL — `Outcome=""` (still using raw Done/IsError), and `finalizeInFlight` signature mismatch (too many args).

- [ ] **Step 3: Rewrite applyResult's terminal assignment**

In `internal/claudestate/state.go`, in `applyResult`, replace the block starting `turn.Done = true` (the three assignments `turn.Done`/`turn.IsError`/`turn.FinishedAt`) with:

```go
	outcome := "completed"
	if p.IsError {
		outcome = "errored"
	}
	setTurnOutcome(turn, outcome, "", ts)
	if p.TotalCostUsd != 0 {
		c := p.TotalCostUsd
		turn.TotalCostUsd = &c
	}
	if len(turn.Blocks) == 0 && p.Result != "" {
		turn.Blocks = []AssistantBlock{{Kind: "text", Text: p.Result}}
	}
```

(Leave the early `if turn.Done { return }` guard above it as-is.)

- [ ] **Step 4: Change finalizeInFlight signature + body**

Replace the `finalizeInFlight` signature and its terminal block:

```go
func (s *SessionState) finalizeInFlight(reason, abortReason, outcome string, ts time.Time) {
	turn := s.lastTurn()
	s.state.InFlight = false
	s.state.Pending = s.state.Pending[:0]
	s.state.PendingQuestions = s.state.PendingQuestions[:0]
	if s.state.Pending == nil {
		s.state.Pending = []ClaudeToolApproval{}
	}
	if s.state.PendingQuestions == nil {
		s.state.PendingQuestions = []ClaudeQuestion{}
	}
	if turn == nil || turn.Done {
		return
	}
	setTurnOutcome(turn, outcome, abortReason, ts)
	if len(turn.Blocks) == 0 && reason != "" {
		turn.Blocks = []AssistantBlock{{Kind: "text", Text: reason}}
	}
	logAbnormalTermination(s.SessionID(), turn.ID, outcome, abortReason, 0)
}
```

> `SessionState` exposes its id via the method `s.SessionID() string` (state.go:43), NOT a struct field.

- [ ] **Step 5: Update the two call sites**

At `:179` (EventClaudeRunEnded → external interrupt):

```go
		s.finalizeInFlight(p.Message, "runner_killed", "aborted", ev.Timestamp)
```

At `:186` (EventClaudeError → classify by code):

```go
		s.state.LastError = &ClaudeError{Code: p.Code, Message: p.Message}
		reason, outcome := classifyClaudeError(p.Code)
		s.finalizeInFlight(p.Message, reason, outcome, ev.Timestamp)
```

And add `classifyClaudeError` to `outcome.go`:

```go
// classifyClaudeError maps a ClaudeError code to (abortReason, outcome).
// Server-shutdown is an external interrupt (aborted); spawn/other CLI
// failures are the CLI's own errors (errored).
func classifyClaudeError(code string) (reason, outcome string) {
	switch code {
	case "server_shutdown":
		return "server_shutdown", "aborted"
	case "claude_spawn_failed":
		return "spawn_failed", "errored"
	default:
		return code, "errored"
	}
}
```

> Confirmed: use `s.SessionID()` (the method at state.go:43) — there is no `s.state.SessionID` field.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/claudestate/ -v`
Expected: PASS — new outcome tests green AND the existing `state_test.go` / `loader_test.go` still green (Done/IsError derived values are unchanged).

- [ ] **Step 7: Commit**

```bash
git add internal/claudestate/state.go internal/claudestate/outcome.go internal/claudestate/outcome_test.go
git commit -m "feat(claudestate): route result + finalizeInFlight through setTurnOutcome with reason"
```

---

### Task 4: Tool result + load-time fixups through setToolOutcome; backfill old snapshots

**Files:**
- Modify: `internal/claudestate/state.go` (`applyToolResult` ~`:260`)
- Modify: `internal/claudestate/loader.go` (`finalizeHangingToolBlocks` `:114`, `finalizeStaleTrailingTurn` `:85`, add backfill in `Load` `:49`)
- Test: `internal/claudestate/loader_test.go` (modify existing hanging-tool tests + add backfill test)

**Interfaces:**
- Consumes: `setToolOutcome`, `setTurnOutcome`, `backfillOutcome`, `logAbnormalTermination`.
- Produces: `func backfillOutcome(t *ClaudeTurn)` in `outcome.go`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/claudestate/loader_test.go`:

```go
func TestBackfillOutcome_OldSnapshot(t *testing.T) {
	// A done turn from before the outcome field existed.
	completed := &ClaudeTurn{ID: "u1", Done: true, IsError: false}
	errored := &ClaudeTurn{ID: "u2", Done: true, IsError: true}
	running := &ClaudeTurn{ID: "u3", Done: false}
	backfillOutcome(completed)
	backfillOutcome(errored)
	backfillOutcome(running)
	if completed.Outcome != "completed" {
		t.Errorf("completed.Outcome=%q", completed.Outcome)
	}
	if errored.Outcome != "errored" {
		t.Errorf("errored.Outcome=%q", errored.Outcome)
	}
	if running.Outcome != "" {
		t.Errorf("running turn should not get an outcome: %q", running.Outcome)
	}
}
```

Modify `TestLoad_StaleTrailingTurn_FinalizesHangingToolBlocks` and `TestLoad_DoneTurn_WithHangingToolBlock_StillFinalizesTool` (in loader_test.go): change the assertion `if tool.Decision == "pending"` to also assert `tool.Outcome == "aborted"`:

```go
	if tool.Outcome != "aborted" {
		t.Errorf("hanging tool should be aborted, got Outcome=%q", tool.Outcome)
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/claudestate/ -run 'TestBackfillOutcome|TestLoad_StaleTrailingTurn_FinalizesHangingToolBlocks|TestLoad_DoneTurn_WithHangingToolBlock' -v`
Expected: FAIL — `undefined: backfillOutcome`, and `tool.Outcome=""`.

- [ ] **Step 3: Add backfillOutcome to outcome.go**

```go
// backfillOutcome derives Outcome for a pre-outcome snapshot turn. Old
// snapshots can't tell errored from aborted (history is gone), so a done
// turn backfills to errored/completed via IsError — both render red, the
// conservative choice. Only newly-produced interrupts get "aborted".
func backfillOutcome(t *ClaudeTurn) {
	if t == nil || t.Outcome != "" || !t.Done {
		return
	}
	if t.IsError {
		t.Outcome = "errored"
	} else {
		t.Outcome = "completed"
	}
}
```

- [ ] **Step 4: Route applyToolResult through setToolOutcome**

In `state.go` `applyToolResult`, replace the three assignments inside the match with:

```go
		if b.Kind == "tool" && b.Tool != nil && b.Tool.ToolUseID == p.ToolUseID {
			b.Tool.Result = p.Content
			outcome := "completed"
			if p.IsError {
				outcome = "errored"
			}
			setToolOutcome(b.Tool, outcome, ts)
			return
		}
```

- [ ] **Step 5: Route finalizeHangingToolBlocks through setToolOutcome**

In `loader.go` `finalizeHangingToolBlocks`, replace the per-tool mutation block (the `if t.Decision == "pending"` … `t.Result = "Interrupted…"`) with:

```go
			t := b.Tool
			if t.Outcome != "" {
				continue // already settled
			}
			if t.Decision == "pending" {
				t.Decision = "deny"
			}
			setToolOutcome(t, "aborted", now)
			if t.Result == "" {
				t.Result = "Interrupted: the runner was killed (server restart) before this tool finished."
			}
			killed = append(killed, t.Name+"("+t.ToolUseID+")")
```

And replace its `slog.Warn(...)` call with the unified logger:

```go
		if len(killed) > 0 {
			logAbnormalTermination("", turn.ID, "aborted", "server_restart", len(killed))
		}
```

- [ ] **Step 6: Route finalizeStaleTrailingTurn through setTurnOutcome**

In `loader.go` `finalizeStaleTrailingTurn`, replace `last.Done = true` / `last.IsError = true` / `last.FinishedAt = &now` with:

```go
	now := time.Now().UTC()
	setTurnOutcome(last, "aborted", "server_restart", now)
```

(Keep the appended "Server restarted…" text block below it.)

- [ ] **Step 7: Add the backfill pass in Load**

In `loader.go` `Load`, immediately after the `finalizeHangingToolBlocks(state.Turns)` call, add:

```go
	for i := range state.Turns {
		backfillOutcome(&state.Turns[i])
	}
```

- [ ] **Step 8: Run the full package**

Run: `go test ./internal/claudestate/ -v`
Expected: PASS — backfill test, the two modified hanging-tool tests, and all pre-existing tests green.

- [ ] **Step 9: Commit**

```bash
git add internal/claudestate/state.go internal/claudestate/loader.go internal/claudestate/outcome.go internal/claudestate/loader_test.go
git commit -m "feat(claudestate): tool outcomes + load-time fixups + old-snapshot backfill"
```

---

### Task 5: TS type mirror

**Files:**
- Modify: `web/src/features/sessions/types.ts` (ClaudeTurn ~`:32-84`, ClaudeToolCall ~`:86-110`)

**Interfaces:**
- Produces: `ClaudeTurn.outcome?`, `ClaudeTurn.abortReason?`, `ClaudeToolCall.outcome?`.

- [ ] **Step 1: Add to ClaudeTurn interface**

In `types.ts`, inside `interface ClaudeTurn`, after the `isError?` line:

```typescript
  isError?: boolean
  /** Terminal state, source of truth. "" / undefined = in progress. */
  outcome?: 'completed' | 'errored' | 'aborted'
  /** Machine-readable interrupt reason (errored/aborted only). */
  abortReason?: string
```

- [ ] **Step 2: Add to ClaudeToolCall interface**

In `interface ClaudeToolCall`, after the `decision` line:

```typescript
  decision: 'allow' | 'deny' | 'pending'
  /** Terminal state. undefined = not terminated. */
  outcome?: 'completed' | 'errored' | 'aborted' | 'denied'
```

- [ ] **Step 3: Verify typecheck**

Run: `cd web && npx tsc --noEmit`
Expected: no new errors (pure additive optional fields).

- [ ] **Step 4: Commit**

```bash
git add web/src/features/sessions/types.ts
git commit -m "feat(web): mirror outcome/abortReason on ClaudeTurn + ClaudeToolCall types"
```

---

### Task 6: Frontend turnPhase + toolStatus read outcome

**Files:**
- Modify: `web/src/features/sessions/ClaudeChatView.tsx` (`turnPhase` `:347-354`, `toolStatus` `:711-721`)
- Test: `web/src/features/sessions/ClaudeChatView.test.tsx`

**Interfaces:**
- Consumes: `ClaudeTurn.outcome`, `ClaudeToolCall.outcome` (Task 5).
- Produces: `turnPhase` returns `'Done' | 'Error' | 'Interrupted' | …`; `toolStatus` returns `'interrupted'` for `outcome === 'aborted'`.

- [ ] **Step 1: Write the failing tests**

Append to `web/src/features/sessions/ClaudeChatView.test.tsx` (it already imports `toolStatus`; add `turnPhase` to the import if exported — see Step 3):

```typescript
  it('toolStatus reads outcome=aborted as interrupted', () => {
    const tool: ClaudeToolCall = {
      toolUseId: 't1', name: 'Edit', decision: 'deny', outcome: 'aborted',
    }
    expect(toolStatus(tool)).toBe('interrupted')
  })

  it('toolStatus reads outcome=errored as errored', () => {
    const tool: ClaudeToolCall = {
      toolUseId: 't2', name: 'Bash', decision: 'allow', outcome: 'errored',
    }
    expect(toolStatus(tool)).toBe('errored')
  })

  it('turnPhase shows Interrupted for an aborted turn', () => {
    expect(turnPhase(makeTurn({ done: true, outcome: 'aborted' }))).toBe('Interrupted')
  })

  it('turnPhase shows Error for an errored turn', () => {
    expect(turnPhase(makeTurn({ done: true, outcome: 'errored' }))).toBe('Error')
  })

  it('turnPhase shows Done for a completed turn', () => {
    expect(turnPhase(makeTurn({ done: true, outcome: 'completed' }))).toBe('Done')
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run ClaudeChatView`
Expected: FAIL — `turnPhase is not a function` (not exported yet) and `toolStatus` returns `'denied'`/`'done'` instead of the outcome-based values.

- [ ] **Step 3: Rewrite toolStatus to switch on outcome**

In `ClaudeChatView.tsx`, replace the `toolStatus` body:

```typescript
export function toolStatus(
  tool: ClaudeToolCall,
): 'pending' | 'denied' | 'interrupted' | 'errored' | 'running' | 'done' {
  switch (tool.outcome) {
    case 'aborted':   return 'interrupted'
    case 'errored':   return 'errored'
    case 'denied':    return 'denied'
    case 'completed': return 'done'
  }
  // Not terminated, or a pre-outcome snapshot: fall back to legacy fields.
  if (tool.decision === 'deny') return 'denied'
  if (tool.result != null) return 'done'
  if (tool.decision === 'allow') return 'running'
  return 'pending'
}
```

- [ ] **Step 4: Rewrite + export turnPhase to switch on outcome**

Replace the `turnPhase` function and add `export`:

```typescript
export function turnPhase(turn: ClaudeTurn): string {
  switch (turn.outcome) {
    case 'completed': return 'Done'
    case 'errored':   return 'Error'
    case 'aborted':   return 'Interrupted'
  }
  if (turn.done) return 'Done' // pre-outcome snapshot fallback
  if (turn.blocks.length === 0) return 'Initializing'
  const last = turn.blocks[turn.blocks.length - 1]
  if (last.kind === 'tool' && !last.tool.finishedAt) return `Calling ${last.tool.name}`
  return 'Thinking'
}
```

Update the test import line to `import { TurnStatsLine, toolStatus, turnPhase } from './ClaudeChatView'`.

- [ ] **Step 5: Add the CSS for the new phase classes**

In `web/src/index.css` near `.turn-phase-chip--done` (~`:122-142`), add:

```css
.turn-phase-chip--error { background: rgba(232, 140, 140, 0.18); color: #e88c8c; }
.turn-phase-chip--interrupted { background: rgba(240, 163, 94, 0.18); color: #f0a35e; }
```

And in `web/src/features/sessions/ClaudeChatView.css` near `.claude-tool--interrupted`, add the errored color:

```css
.claude-tool--errored .claude-tool__status { color: #e88c8c; }
```

- [ ] **Step 6: Run the tests**

Run: `cd web && npx vitest run`
Expected: PASS — new turnPhase/toolStatus tests AND the pre-existing 137 tests still green (legacy `interrupted` tests now satisfied via outcome OR the fallback).

- [ ] **Step 7: Commit**

```bash
git add web/src/features/sessions/ClaudeChatView.tsx web/src/features/sessions/ClaudeChatView.test.tsx web/src/index.css web/src/features/sessions/ClaudeChatView.css
git commit -m "feat(web): turnPhase + toolStatus read outcome (Done/Error/Interrupted)"
```

---

### Task 7: Reducer sets outcome on live frames

**Files:**
- Modify: `web/src/features/sessions/claudeReducer.ts` (`finalizeInFlightTurn` `:531-551`, `result` case `:350-365`, `tool_result` case `:331-341`)
- Test: `web/src/features/sessions/useSessions.test.ts` or a focused reducer test

**Interfaces:**
- Consumes: `ClaudeTurn.outcome`, `ClaudeToolCall.outcome` (Task 5).
- Produces: live WS frames stamp `outcome` so a still-connected client matches what the backend persisted.

- [ ] **Step 1: Write the failing test**

Append to `web/src/features/sessions/ClaudeChatView.test.tsx` (reuse the existing test harness) OR create a focused test importing the reducer. Minimal focused test in a new file `web/src/features/sessions/claudeReducer.outcome.test.ts`:

```typescript
import { describe, it, expect } from 'vitest'
import { finalizeInFlightTurn } from './claudeReducer'
import type { ClaudeState } from './types'

function base(): ClaudeState {
  return {
    turns: [{ id: 'u1', prompt: 'p', startedAt: new Date().toISOString(), blocks: [], done: false }],
    inFlight: true, pending: [], pendingQuestions: [], bgTasks: {}, subagents: {}, bgTaskLogs: {},
  }
}

describe('finalizeInFlightTurn outcome', () => {
  it('marks the in-flight turn aborted', () => {
    const next = finalizeInFlightTurn(base(), 'runner died', '2026-06-20T00:00:00Z')
    expect(next.turns[0].outcome).toBe('aborted')
    expect(next.inFlight).toBe(false)
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npx vitest run claudeReducer.outcome`
Expected: FAIL — `outcome` is `undefined`.

- [ ] **Step 3: Set outcome in finalizeInFlightTurn**

In `claudeReducer.ts` `finalizeInFlightTurn`, in the `const last = { ...turns[lastIdx], … }` object, add `outcome: 'aborted' as const` alongside `done`/`isError`:

```typescript
    const last = {
      ...turns[lastIdx],
      done: true,
      isError: true,
      outcome: 'aborted' as const,
      finishedAt: ts ?? new Date().toISOString(),
    }
```

- [ ] **Step 4: Set outcome in the result case**

In the `case 'result':` block, after `last.isError = p.isError`, add:

```typescript
    last.outcome = p.isError ? 'errored' : 'completed'
```

- [ ] **Step 5: Set tool outcome in tool_result**

In the `case 'tool_result':` block, in the `patchToolBlock` updater, add `outcome`:

```typescript
    last.blocks = patchToolBlock(last.blocks, p.toolUseId, (t) => ({
      ...t,
      result: p.content,
      isError: p.isError,
      outcome: p.isError ? ('errored' as const) : ('completed' as const),
      finishedAt: asTimestamp(frameTs),
    }))
```

- [ ] **Step 6: Run all frontend tests**

Run: `cd web && npx vitest run`
Expected: PASS — new reducer test + all prior tests.

- [ ] **Step 7: Commit**

```bash
git add web/src/features/sessions/claudeReducer.ts web/src/features/sessions/claudeReducer.outcome.test.ts
git commit -m "feat(web): reducer stamps outcome on result/tool_result/finalize frames"
```

---

### Task 8: End-to-end verification + docs

**Files:**
- Modify: `CONTEXT.md` (update the hanging-tool trap row added earlier to reference the outcome model)
- Modify: `docs/superpowers/specs/2026-06-20-turn-tool-outcome-semantics-design.md` (mark Status: Implemented)

- [ ] **Step 1: Full backend + frontend suites green**

Run: `go test ./internal/claudestate/ && cd web && npx vitest run`
Expected: both green.

- [ ] **Step 2: Live rebuild + verify the interrupt log fires**

Run: `make local-dev` then trigger/inspect a restart-finalized session:

```bash
grep "terminated abnormally" /tmp/alfred-server.log | jq '{turnId, outcome, reason, hangingTools}'
```
Expected: at least one line with `outcome:"aborted"`, a `reason`, and a `hangingTools` count. (The previously-stuck session 1 should show `reason:"server_restart"`.)

- [ ] **Step 3: Update the CONTEXT.md trap row**

In `CONTEXT.md`, in the hanging-tool-block trap row, append a sentence:

```
The fix is now part of the unified outcome model (spec 2026-06-20): hanging tools get `setToolOutcome(_, "aborted", _)` and the turn gets `aborted` + `abortReason="server_restart"`, logged via `logAbnormalTermination`. A `Done` turn must never contain an unsettled tool (`Outcome==""`).
```

- [ ] **Step 4: Mark the spec Implemented**

Change the spec's `**Status:**` line to `Implemented (next branch)`.

- [ ] **Step 5: Commit**

```bash
git add CONTEXT.md docs/superpowers/specs/2026-06-20-turn-tool-outcome-semantics-design.md
git commit -m "docs(context): fold hanging-tool trap into the outcome model; mark spec implemented"
```

---

## Notes for the implementer

- **`SessionState.SessionID()` is a method** (state.go:43), already reflected in Task 3. The load-time fixups (loader.go) have no SessionState in scope, so they pass `""` for sessionId — that is intentional and acceptable.
- **The legacy `interrupted` tests** added in the earlier hanging-tool fix (`result.startsWith('Interrupted')`) are superseded by the outcome-based path. In Task 6 they keep passing via the `toolStatus` legacy fallback, but if they conflict, update them to set `outcome: 'aborted'` instead of relying on the result string.
- **Do not bump the snapshot version.** The fields are additive and backfilled.
