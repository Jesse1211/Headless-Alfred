package claudestate

import (
	"context"
	"fmt"
	"sync"
	"time"
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

	mu       sync.RWMutex
	state    ClaudeState
	curIndex *perTurnIndex // transient; not serialized

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

// ClaudeUUID returns the Claude CLI session uuid this state was
// constructed with. Treated as immutable for now: rotation (e.g.
// /compact) is not currently propagated into SessionState — when we
// implement that path it'll need to coordinate with Persister's
// snapshot header and the jsonl Locator cache, so the setter was
// removed to keep the contract honest.
func (s *SessionState) ClaudeUUID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claudeUUID
}

// View runs fn against the state under the read lock. fn MUST NOT
// retain a reference to the *ClaudeState after returning; copy out
// what is needed and let the lock release.
func (s *SessionState) View(fn func(*ClaudeState)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.state)
}

// AttachPersister installs p so subsequent Apply calls signal
// dirty. SessionManager (Plan 2) wires this immediately after
// NewSessionState. Test code can leave it unset to keep tests
// hermetic.
func (s *SessionState) AttachPersister(p *Persister) {
	s.persister = p
}

// markDirty fires the Persister dirty bit. Called from Apply and
// BeginTurn after every mutation. Cheap (single channel send) so
// we don't bother filtering on "did state actually change."
func (s *SessionState) markDirty() {
	if s.persister != nil {
		s.persister.MarkDirty()
	}
}

// Close flushes the Persister (if attached) and stops its goroutine.
// Safe to call when persister is nil (test path).
func (s *SessionState) Close(ctx context.Context) error {
	if s.persister == nil {
		return nil
	}
	return s.persister.Close(ctx)
}

// CloseNoFlush stops the Persister without a final write. Use when
// the snapshot directory has already been removed (session was
// deleted) so a final write would just produce misleading ENOENT
// errors. Safe to call when persister is nil.
func (s *SessionState) CloseNoFlush(ctx context.Context) error {
	if s.persister == nil {
		return nil
	}
	return s.persister.CloseNoFlush(ctx)
}

// BeginTurn appends a fresh turn to the conversation. Called from the
// claude_prompt entry point (Plan 2) to optimistically register the
// user's outgoing prompt before any stream events arrive.
func (s *SessionState) BeginTurn(id, prompt string, startedAt time.Time) {
	s.mu.Lock()
	defer s.markDirty()
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
	defer s.markDirty()
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
			// ADR-001: a terminator with a malformed payload must still
			// finalize the in-flight turn. Returning the error without a
			// finalize would strand the turn (Done=false, InFlight=true)
			// forever — a permanent spinner. Synthetically finalize, then
			// surface the error.
			s.finalizeInFlight("terminated abnormally: bad result payload", ev.Timestamp)
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.applyResult(p, ev.Timestamp)
	case EventClaudeRunEnded:
		p, ok := ev.Payload.(*ClaudeRunEndedPayload)
		if !ok {
			// ADR-001: synthetic finalize before erroring so a malformed
			// run-ended terminator still unlocks the composer.
			s.finalizeInFlight("terminated abnormally: bad run_ended payload", ev.Timestamp)
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.finalizeInFlight(p.Message, ev.Timestamp)
	case EventClaudeError:
		p, ok := ev.Payload.(*ClaudeErrorPayload)
		if !ok {
			// ADR-001: synthetic finalize before erroring so a malformed
			// error terminator still unwinds the in-flight turn.
			s.finalizeInFlight("terminated abnormally: bad claude_error payload", ev.Timestamp)
			return fmt.Errorf("claudestate.Apply: bad payload for %s", ev.Kind)
		}
		s.state.LastError = &ClaudeError{Code: p.Code, Message: p.Message}
		s.finalizeInFlight(p.Message, ev.Timestamp)
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
	default:
		// Unknown event kind; silently no-op so future versions
		// don't crash on older runners.
	}
	return nil
}

// applyTextDelta appends text to the block at `index` within the
// current turn. Uses the per-turn index map to track content-block
// index → position mapping, accounting for message_start resets.
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
	turn.Usage = &TokenUsage{
		InputTokens:              p.Usage.InputTokens,
		OutputTokens:             p.Usage.OutputTokens,
		CacheReadInputTokens:     p.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: p.Usage.CacheCreationInputTokens,
	}
}

func (s *SessionState) applyResult(p *ResultPayload, ts time.Time) {
	turn := s.lastTurn()
	s.state.InFlight = false
	if turn == nil {
		return
	}
	// Don't overwrite a turn that finalizeInFlight already closed
	// (shutdown finalize, claude_error, claude_run_ended) — a late
	// result event arriving after the synthetic close would otherwise
	// flip IsError back to false and replace the recorded error text
	// with success-looking cost/finishedAt fields. The earlier
	// terminator wins; we just leave InFlight=false in place.
	if turn.Done {
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
	// Clear but keep non-nil slices — the JSON wire format must
	// stay `[]` not `null` (frontend reads .length).
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
	turn.Done = true
	turn.IsError = true
	turn.FinishedAt = timePtr(ts)
	if len(turn.Blocks) == 0 && reason != "" {
		turn.Blocks = []AssistantBlock{{Kind: "text", Text: reason}}
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

// perTurnIndex tracks the per-message content-block index → array
// position mapping for the current in-progress turn. Reset on each
// EventMessageStart. The map is keyed by Apply-time turn ID so a
// turn spanning many messages keeps cumulative blocks but each
// message's index space is fresh.
type perTurnIndex struct {
	turnID   string
	blocks   map[int]int // content-block index → position in Turn.Blocks
	thinking map[int]int // content-block index → position in Turn.Thinking
}

// indexFor returns (and lazily creates) the index map for the active
// turn. Called under write lock. When the active turn changes (next
// turn began), a fresh map replaces the old one.
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

// ---- task lifecycle reducers ----

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
	status, _ := p.Patch["status"].(string)
	if status != "completed" && status != "failed" &&
		status != "killed" && status != "stopped" {
		return
	}
	bt.Status = status
	if et, ok := p.Patch["end_time"].(float64); ok && et > 0 {
		bt.FinishedAt = timePtr(time.Unix(0, int64(et)*int64(time.Millisecond)).UTC())
	} else {
		bt.FinishedAt = timePtr(ts)
	}
	if status == "killed" {
		bt.LastEventSummary = "killed by Claude on turn end"
	}
	s.state.BgTasks[p.TaskID] = bt
}

// ---- hook lifecycle reducers ----

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

// ---- tool decision reducer ----

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
