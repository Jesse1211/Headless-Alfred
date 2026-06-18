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
		// Remaining kinds wired in Task 6 (message_start) and Task 7
		// (lifecycle). Until then they're a no-op so integration tests
		// can submit them without crashing.
	}
	return nil
}

// applyTextDelta appends text to the block at `index` within the
// current turn. Holds the write lock; caller is responsible for
// acquiring it.
//
// At Task 5 we use a naive "find a text block at or after position
// index" lookup that's correct for single-message turns. Task 6
// replaces this with a real per-turn index map keyed off
// message_start resets.
func (s *SessionState) applyTextDelta(p *TextDeltaPayload) {
	turn := s.lastTurn()
	if turn == nil || turn.Done {
		return
	}
	pos, ok := lookupBlockPos(turn, p.Index, "text")
	if !ok || turn.Blocks[pos].Kind != "text" {
		turn.Blocks = append(turn.Blocks, AssistantBlock{Kind: "text"})
		pos = len(turn.Blocks) - 1
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

// lookupBlockPos is a Task-5 stub: scan the blocks slice for one
// matching kind at-or-after the index. Task 6 replaces this with a
// real index map keyed off message_start resets.
func lookupBlockPos(turn *ClaudeTurn, index int, want string) (int, bool) {
	for i, b := range turn.Blocks {
		if b.Kind == want && i >= index {
			return i, true
		}
	}
	return -1, false
}
