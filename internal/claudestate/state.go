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
