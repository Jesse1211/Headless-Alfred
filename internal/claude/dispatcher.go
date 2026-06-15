package claude

import (
	"log/slog"
	"sync"
)

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
// Three failure modes, each with a different resolution:
//
//   - Unknown claude convo (lookup returns "") → autoAllow. This is
//     a foreign claude process running on the same machine (think:
//     user typing `claude` in another terminal while Alfred is up).
//     If we denied, we'd globally break every claude on the box.
//     Hook fail-open is the correct default.
//
//   - Alfred session known but no WS subscriber → autoDeny. The user
//     started Claude UI on this session, then closed all tabs. Without
//     somebody to approve, we can't run the tool — and we DON'T want
//     to silently allow, because the user opted into the
//     ask-before-each contract.
//
//   - Subscriber channel full → autoDeny. The user has more pending
//     approvals than the buffer holds; new ones can't queue. Denying
//     keeps the bridge unblocked.
//
// dataDir is the server's data root (Manager.DataDir()). It feeds
// the summary fast-path: tool calls that match isSummaryIO for the
// matched Alfred session skip the WS subscriber entirely and are
// auto-allowed. This avoids popping an approval card every turn for
// the summary-todo template's Read+Write churn. Pass "" to disable
// the fast-path (used by tests that exercise the normal flow).
func (d *Dispatcher) OnAsk(
	lookup func(claudeConvoID string) string,
	autoAllow func(toolUseID string),
	autoDeny func(toolUseID string, reason string),
	dataDir string,
) func(PendingRequest) {
	return func(req PendingRequest) {
		alfredSID := lookup(req.SessionID)
		if alfredSID == "" {
			// Foreign claude session. Let it through.
			slog.Debug("dispatcher: not an Alfred session, auto-allow", "claudeConvoID", req.SessionID, "tool", req.ToolName)
			autoAllow(req.ToolUseID)
			return
		}
		// Summary template's per-turn Read+Write of the canonical
		// summary file. Strict path + tool + size check inside;
		// anything off-pattern still goes through the WS card.
		if dataDir != "" && isSummaryIO(req, alfredSID, dataDir) {
			autoAllow(req.ToolUseID)
			return
		}
		d.mu.Lock()
		ch, ok := d.subs[alfredSID]
		d.mu.Unlock()
		if !ok {
			slog.Warn("dispatcher: no UI client subscribed, auto-deny", "session", alfredSID, "tool", req.ToolName, "toolUseID", req.ToolUseID)
			autoDeny(req.ToolUseID,
				"no UI client subscribed for session "+alfredSID)
			return
		}
		select {
		case ch <- req:
			slog.Debug("dispatcher: routed approval to UI", "session", alfredSID, "tool", req.ToolName, "toolUseID", req.ToolUseID)
		default:
			slog.Warn("dispatcher: queue full, auto-deny", "session", alfredSID, "tool", req.ToolName, "toolUseID", req.ToolUseID)
			autoDeny(req.ToolUseID, "approval queue full for this session")
		}
	}
}
