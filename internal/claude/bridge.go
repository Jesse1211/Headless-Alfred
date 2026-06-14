package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Bridge backs the PreToolUse hook claude invokes before running a
// tool. Architecture:
//
//	claude (child)
//	    ↓ exec
//	/usr/local/bin/alfred-claude-bridge   (a tiny shell script
//	                                       shipped in the Dockerfile)
//	    ↓ POST localhost:8090/tool-approval (request body = hook stdin)
//	bridge.go (this file)
//	    ↓ blocks until ws.go pushes a decision via Resolve(toolUseID, decision)
//	    ↓ HTTP 200, body = {"permissionDecision": "allow"|"deny"}
//	bridge script writes that body to its own stdout
//	    ↓
//	claude reads it, proceeds or skips the tool
//
// The bridge listens on 127.0.0.1 only — we trust anything talking
// to it from inside the pod. Auth would be redundant; the only
// caller is our hook script running as the same alfred user.
//
// Pending requests are keyed by tool_use_id, which the CLI
// guarantees is unique per Claude invocation. ws.go correlates
// tool_use_id back to an Alfred session via the session_id field
// in the hook input.
type Bridge struct {
	mu       sync.Mutex
	pending  map[string]*PendingRequest
	onAsk    func(req PendingRequest) // called outside the lock
	server   *http.Server
	listener net.Listener

	// AskTimeout is how long a pending request waits for a
	// decision before auto-denying. Defaults to 5 minutes.
	AskTimeout time.Duration
}

// PendingRequest is the hook payload + a channel for the eventual
// decision. ws.go reads the request via onAsk and writes the
// decision via Resolve.
type PendingRequest struct {
	ToolUseID      string          `json:"tool_use_id"`
	SessionID      string          `json:"session_id"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	CWD            string          `json:"cwd"`
	PermissionMode string          `json:"permission_mode"`
	TranscriptPath string          `json:"transcript_path"`

	// resolved fires once when Resolve is called. Buffered=1 so the
	// resolver doesn't block.
	resolved chan Decision
}

// Decision is the result of a tool-use approval request.
//
// Reason is included in the JSON returned to claude as
// {"permissionDecision":"deny","permissionDecisionReason":"<reason>"}
// for deny cases; allow cases ignore Reason.
type Decision struct {
	Permission string // "allow" | "deny"
	Reason     string
}

// NewBridge constructs a Bridge. onAsk is called every time a hook
// posts a new tool-use request; the function should be non-blocking
// (e.g., push the request onto a channel and return).
func NewBridge(onAsk func(PendingRequest)) *Bridge {
	return &Bridge{
		pending:    map[string]*PendingRequest{},
		onAsk:      onAsk,
		AskTimeout: 5 * time.Minute,
	}
}

// Start binds a listener on 127.0.0.1:port and serves the HTTP API
// in a goroutine. Pass port = 0 to let the OS pick a free port;
// the chosen port is reported by Addr().
//
// In production we want a fixed port (the hook script needs to know
// where to POST). 8090 is the planned default.
func (b *Bridge) Start(ctx context.Context, port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bridge listen %s: %w", addr, err)
	}
	b.listener = ln

	mux := http.NewServeMux()
	mux.HandleFunc("/tool-approval", b.handleAsk)
	mux.HandleFunc("/healthz", b.handleHealth)
	b.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = b.server.Serve(ln)
	}()
	go func() {
		<-ctx.Done()
		// Give in-flight requests a brief moment to drain. They're
		// all blocking on Resolve, so they won't actually drain —
		// but Shutdown will close their connections promptly.
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.server.Shutdown(shutCtx)
	}()
	return nil
}

// Addr returns the bridge's listening address (host:port). Useful
// for tests that bind to port 0.
func (b *Bridge) Addr() string {
	if b.listener == nil {
		return ""
	}
	return b.listener.Addr().String()
}

// Resolve completes a pending request. Safe to call before, during,
// or after Start (called before = no-op; before would only happen on
// a race we don't expect in practice).
//
// Returns false if no pending request matches the toolUseID; this
// happens if the decision arrives after AskTimeout, or for a
// duplicate Resolve.
func (b *Bridge) Resolve(toolUseID string, decision Decision) bool {
	b.mu.Lock()
	pr, ok := b.pending[toolUseID]
	if ok {
		delete(b.pending, toolUseID)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	// Buffered channel; non-blocking.
	pr.resolved <- decision
	return true
}

// handleAsk receives one tool-use request and blocks until a
// decision arrives (or AskTimeout elapses, or the client/conn closes).
// Concurrent asks are independent — Bridge holds one pending entry
// per tool_use_id.
func (b *Bridge) handleAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	req := PendingRequest{resolved: make(chan Decision, 1)}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.ToolUseID == "" || req.SessionID == "" {
		http.Error(w, "missing tool_use_id or session_id", http.StatusBadRequest)
		return
	}

	b.mu.Lock()
	if _, dup := b.pending[req.ToolUseID]; dup {
		b.mu.Unlock()
		// Same tool_use_id is in flight already. This shouldn't
		// happen in normal claude behavior — defensive 409.
		http.Error(w, "duplicate tool_use_id", http.StatusConflict)
		return
	}
	b.pending[req.ToolUseID] = &req
	b.mu.Unlock()

	// Notify ws.go. The callback should be fast — push to a
	// channel and return.
	b.onAsk(req)

	// Block.
	select {
	case d := <-req.resolved:
		writeHookResponse(w, d)
	case <-time.After(b.AskTimeout):
		// Auto-deny on timeout.
		b.mu.Lock()
		delete(b.pending, req.ToolUseID)
		b.mu.Unlock()
		writeHookResponse(w, Decision{Permission: "deny", Reason: "timed out waiting for user decision"})
	case <-r.Context().Done():
		// Client (hook process) went away. We have no decision to
		// send; just give up. The hook will see the closed
		// connection, exit non-zero, and claude will treat that as
		// a block. Same end effect as deny.
		b.mu.Lock()
		delete(b.pending, req.ToolUseID)
		b.mu.Unlock()
		return
	}
}

func (b *Bridge) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

// writeHookResponse encodes the decision in the JSON shape claude
// expects from a PreToolUse hook command.
func writeHookResponse(w http.ResponseWriter, d Decision) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]string{"permissionDecision": d.Permission}
	if d.Permission == "deny" && d.Reason != "" {
		resp["permissionDecisionReason"] = d.Reason
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// Stop closes the listener and aborts any pending requests
// (they'll see context-cancellation). Safe to call multiple times.
func (b *Bridge) Stop() error {
	if b.server == nil {
		return nil
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := b.server.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
