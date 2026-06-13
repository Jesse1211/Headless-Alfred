package claude

import "sync"

// Dispatcher routes PendingRequests from the bridge (which only knows
// Claude's session_id) to WS clients (which are keyed by Alfred's
// sessionID). Wired in main.go:
//
//	bridge := claude.NewBridge(disp.OnAsk(manager.FindByClaudeConvoID))
//	wsHandler subscribes via disp.SubscribeAsks(alfredSessionID)
//
// One subscriber per Alfred session at a time. Subscribe replaces the
// previous channel if any (typical: a reconnecting WS client claims
// the slot from a stale one).
type Dispatcher struct {
	mu   sync.Mutex
	subs map[string]chan PendingRequest
}

// NewDispatcher returns an empty dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{subs: map[string]chan PendingRequest{}}
}

// SubscribeAsks registers a channel that will receive tool-approval
// requests for the given Alfred sessionID. Returns the channel plus
// an unregister function. Buffer is 4 (a busy Claude can queue a
// handful of parallel tool calls; we want to absorb the burst rather
// than block the bridge HTTP handler).
//
// If a previous subscriber existed for the same sessionID, its
// channel is closed and the new one takes over. This mirrors the
// existing single-active-WS-per-session expectation in V0.
func (d *Dispatcher) SubscribeAsks(sessionID string) (<-chan PendingRequest, func()) {
	ch := make(chan PendingRequest, 4)
	d.mu.Lock()
	if prev, ok := d.subs[sessionID]; ok {
		// Replace; close the prior to wake any selectors.
		close(prev)
	}
	d.subs[sessionID] = ch
	d.mu.Unlock()
	return ch, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if cur, ok := d.subs[sessionID]; ok && cur == ch {
			delete(d.subs, sessionID)
			close(ch)
		}
	}
}

// OnAsk returns an onAsk callback suitable for passing to NewBridge.
// The callback uses lookup to translate Claude's session_id (in
// req.SessionID) into an Alfred sessionID, then pushes to the
// matching subscriber channel.
//
// If no subscriber is registered (the user has closed all browser
// tabs, or the Alfred session doesn't exist), the request is
// auto-denied via deny — we have nobody to ask. The
// alfred-session-not-found path SHOULD NOT happen in normal
// operation (bridge only gets called from a claude that we spawned).
func (d *Dispatcher) OnAsk(lookup func(claudeConvoID string) string,
	autoDeny func(toolUseID string, reason string)) func(PendingRequest) {
	return func(req PendingRequest) {
		alfredSID := lookup(req.SessionID)
		if alfredSID == "" {
			autoDeny(req.ToolUseID,
				"no Alfred session for claude convo "+req.SessionID)
			return
		}
		d.mu.Lock()
		ch, ok := d.subs[alfredSID]
		d.mu.Unlock()
		if !ok {
			autoDeny(req.ToolUseID,
				"no UI client subscribed for session "+alfredSID)
			return
		}
		select {
		case ch <- req:
		default:
			// Subscriber channel full — typical only if the user
			// has queued more than 4 tool calls without acting on
			// any. Auto-deny to keep the bridge unblocked.
			autoDeny(req.ToolUseID, "approval queue full for this session")
		}
	}
}
