package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/oklog/ulid/v2"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/store"
)

const (
	maxCommandBytes   = 4096
	maxInboundMessage = 8 * 1024
	readDeadline      = 60 * time.Second
	pingInterval      = 20 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		host := r.Host
		for _, prefix := range []string{"https://", "http://"} {
			if strings.HasPrefix(origin, prefix) {
				return strings.TrimPrefix(origin, prefix) == host
			}
		}
		return false
	},
}

// bridge / dispatcher accept nil for the legacy V0 path / tests that
// don't use claude UI; callers that want claude UI must pass both.
func WSHandler(m *session.Manager, a auth.Auth, bridge *claude.Bridge, disp *claude.Dispatcher) http.Handler {
	runner := claude.NewRunner()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Query().Get("token")
		if !a.VerifyToken(tok) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("ws upgrade", "err", err)
			return
		}
		runClientLoop(conn, m, bridge, disp, runner)
	})
}

// claudeRunState tracks one in-flight `claude -p` invocation per
// alfred session. Stored in a per-WS-connection map; lifetime ends
// when the runner exits or the user clicks Stop.
type claudeRunState struct {
	cancel context.CancelFunc
	stop   func()
}

func runClientLoop(conn *websocket.Conn, m *session.Manager, bridge *claude.Bridge, disp *claude.Dispatcher, runner *claude.Runner) {
	defer conn.Close()
	conn.SetReadLimit(maxInboundMessage)
	_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		return nil
	})

	writeMu := &sync.Mutex{}
	write := func(msg OutMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(msg)
	}

	sessions := m.List()
	subs := make([]NamedSubscriber, 0, len(sessions))
	cancels := []func(){}
	// ptyChunks carries raw PTY bytes (claude mode). It's a single shared
	// channel per WS connection; each session's raw subscriber pushes
	// {sessionID, bytes} onto it via a small forwarder goroutine.
	ptyChunks := make(chan ptyChunk, 64)
	stop := make(chan struct{})
	defer close(stop)
	for _, meta := range sessions {
		sh, err := m.Get(meta.ID)
		if err != nil {
			continue
		}
		sub, cancel := sh.SubscribeEvents(16)
		subs = append(subs, NamedSubscriber{SessionID: meta.ID, Sub: sub})
		cancels = append(cancels, cancel)
		// Also subscribe to raw bytes for potential claude-mode output.
		rawSub := sh.SubscribeRaw(64)
		cancels = append(cancels, rawSub.Close)
		go forwardRaw(meta.ID, rawSub, ptyChunks, stop)
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	for _, meta := range sessions {
		sh, err := m.Get(meta.ID)
		if err != nil {
			continue
		}
		mode := string(m.GetMode(meta.ID))
		if mode == "" {
			mode = "shell"
		}
		renderer := string(m.GetRenderer(meta.ID))
		cur := sh.CurrentCommand()
		if cur == nil {
			_ = write(OutMsg{Type: "idle", SessionID: meta.ID, Mode: mode, Renderer: renderer})
		} else {
			_ = write(OutMsg{
				Type:        "reattach",
				SessionID:   meta.ID,
				CmdID:       cur.ID,
				Command:     cur.Command,
				StartedAt:   cur.StartedAt.UTC().Format(time.RFC3339Nano),
				OutputSoFar: base64.StdEncoding.EncodeToString(cur.Buffer),
				Mode:        mode,
				Renderer:    renderer,
			})
		}
	}

	closedCh := make(chan string, 4)
	renamedCh := make(chan namedRename, 4)
	createdCh := make(chan string, 4)
	removeClose := m.AddCloseListener(func(sid string) {
		select {
		case closedCh <- sid:
		default:
		}
	})
	removeRename := m.AddRenameListener(func(sid, name string) {
		select {
		case renamedCh <- namedRename{ID: sid, Name: name}:
		default:
		}
	})
	removeCreate := m.AddCreateListener(func(sid string) {
		select {
		case createdCh <- sid:
		default:
			slog.Warn("ws: createdCh full, dropping created event", "session", sid)
		}
	})
	defer removeClose()
	defer removeRename()
	defer removeCreate()

	events := make(chan FanInEvent, 64)
	go FanIn(subs, events, stop)

	// Claude UI per-connection state.
	asks := make(chan claude.PendingRequest, 16)
	claudeEvents := make(chan claudeEvtForward, 64)
	claudeRunStates := map[string]*claudeRunState{}
	defer func() {
		// On disconnect, stop any still-running claude prompts so we
		// don't leak processes.
		for _, st := range claudeRunStates {
			if st.stop != nil {
				st.stop()
			}
			if st.cancel != nil {
				st.cancel()
			}
		}
	}()

	// Subscribe each existing session to the bridge's ask dispatcher.
	// Forwards to the per-WS asks channel.
	if disp != nil {
		for _, meta := range sessions {
			subCh, unsub := disp.SubscribeAsks(meta.ID)
			cancels = append(cancels, unsub)
			go forwardAsks(subCh, asks, stop)
		}
	}

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	inbound := make(chan InMsg, 4)
	go func() {
		for {
			var msg InMsg
			if err := conn.ReadJSON(&msg); err != nil {
				close(inbound)
				return
			}
			select {
			case inbound <- msg:
			case <-stop:
				return
			}
		}
	}()

	for {
		select {
		case <-pingTicker.C:
			_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
		case msg, ok := <-inbound:
			if !ok {
				return
			}
			handleInbound(msg, m, bridge, runner, claudeEvents, claudeRunStates, write)
		case ev, ok := <-events:
			if !ok {
				return
			}
			// In claude mode, sentinel-derived frames (started/chunk/done)
			// are suppressed — the user sees raw PTY bytes via pty_data
			// instead. The parser keeps running so nothing breaks; we
			// just don't ship those frames to the client while in claude.
			if m.GetMode(ev.SessionID) == store.ModeClaude {
				continue
			}
			writeEventToClient(ev, write)
		case ptyCh := <-ptyChunks:
			// Raw PTY bytes only ship in claude mode. In shell mode the
			// equivalent bytes are already being parsed into chunk frames
			// (above), so shipping them as pty_data too would double-send.
			if m.GetMode(ptyCh.sessionID) != store.ModeClaude {
				continue
			}
			_ = write(OutMsg{
				Type:      "pty_data",
				SessionID: ptyCh.sessionID,
				Data:      base64.StdEncoding.EncodeToString(ptyCh.data),
			})
		case req := <-asks:
			// PreToolUse hook fired. Push to the user for Allow/Deny.
			alfredSID := m.FindByClaudeConvoID(req.SessionID)
			if alfredSID == "" {
				// Shouldn't happen — dispatcher already looked this up
				// to find us — but degrade gracefully.
				if bridge != nil {
					bridge.Resolve(req.ToolUseID, claude.Decision{
						Permission: "deny",
						Reason:     "session vanished",
					})
				}
				continue
			}
			var inputAny any
			_ = json.Unmarshal(req.ToolInput, &inputAny)
			_ = write(OutMsg{
				Type:      "tool_approval_request",
				SessionID: alfredSID,
				ToolUseID: req.ToolUseID,
				Tool:      req.ToolName,
				ToolInput: inputAny,
			})
		case fwd := <-claudeEvents:
			// One stream-json event from an in-flight claude -p
			// invocation. Push as a claude_event frame.
			_ = write(OutMsg{
				Type:      "claude_event",
				SessionID: fwd.sessionID,
				EventKind: string(fwd.kind),
				Payload:   fwd.payload,
			})
		case sid := <-closedCh:
			_ = write(OutMsg{Type: "session_closed", SessionID: sid})
		case rn := <-renamedCh:
			_ = write(OutMsg{Type: "session_renamed", SessionID: rn.ID, Name: rn.Name})
		case sid := <-createdCh:
			// New session was just created via REST. Subscribe to its
			// events and forward them onto the existing FanIn channel
			// so they reach this WS client without forcing a reconnect.
			// Also send an "idle" frame so the client knows the
			// subscription is live (mirrors the on-connect handshake).
			sh, err := m.Get(sid)
			if err != nil {
				continue
			}
			sub, cancel := sh.SubscribeEvents(16)
			cancels = append(cancels, cancel)
			go forwardSubscriber(sid, sub, events, stop)
			rawSub := sh.SubscribeRaw(64)
			cancels = append(cancels, rawSub.Close)
			go forwardRaw(sid, rawSub, ptyChunks, stop)
			if disp != nil {
				subCh, unsub := disp.SubscribeAsks(sid)
				cancels = append(cancels, unsub)
				go forwardAsks(subCh, asks, stop)
			}
			_ = write(OutMsg{Type: "idle", SessionID: sid, Mode: "shell"})
		}
	}
}

// ptyChunk carries raw PTY bytes from a claude-mode session to the
// WS write loop.
type ptyChunk struct {
	sessionID string
	data      []byte
}

// claudeEvtForward carries one parsed claude stream-json event from
// a per-session runner goroutine to the WS write loop.
type claudeEvtForward struct {
	sessionID string
	kind      claude.EventKind
	payload   any
}

// forwardAsks pumps PendingRequests from the dispatcher's per-session
// channel onto the per-WS shared asks channel.
func forwardAsks(in <-chan claude.PendingRequest, out chan<- claude.PendingRequest, stop <-chan struct{}) {
	for {
		select {
		case req, ok := <-in:
			if !ok {
				return
			}
			select {
			case out <- req:
			case <-stop:
				return
			}
		case <-stop:
			return
		}
	}
}

// forwardClaudeRunner reads parsed events from a Runner's channel and
// ships each onto the per-WS claudeEvents channel for serialization.
// Closes silently when the source channel closes.
func forwardClaudeRunner(sessionID string, src <-chan claude.Event, out chan<- claudeEvtForward, stop <-chan struct{}) {
	for {
		select {
		case ev, ok := <-src:
			if !ok {
				return
			}
			payload := claudeEventPayload(ev)
			select {
			case out <- claudeEvtForward{sessionID: sessionID, kind: ev.Kind, payload: payload}:
			case <-stop:
				return
			}
		case <-stop:
			return
		}
	}
}

// claudeEventPayload extracts the concrete variant payload from an
// Event for JSON marshalling. Returns nil for variants with no
// payload (e.g. MessageStop).
func claudeEventPayload(ev claude.Event) any {
	switch ev.Kind {
	case claude.KindSystem:
		return ev.System
	case claude.KindRateLimit:
		return ev.RateLimit
	case claude.KindTextDelta:
		return ev.TextDelta
	case claude.KindTextBlockEnd:
		return ev.TextBlockEnd
	case claude.KindToolUseStart:
		return ev.ToolUseStart
	case claude.KindToolUseEnd:
		return ev.ToolUseEnd
	case claude.KindToolResult:
		return ev.ToolResult
	case claude.KindMessageStart:
		return ev.MessageStart
	case claude.KindMessageDelta:
		return ev.MessageDelta
	case claude.KindMessageStop:
		return nil
	case claude.KindResult:
		return ev.Result
	case claude.KindUnknown:
		return ev.Unknown
	}
	return nil
}

// forwardRaw pumps raw PTY bytes from the shell's raw broadcaster
// onto the per-WS ptyChunks channel until either the subscriber's
// channel closes or stop closes. Drop messages (from the broadcaster's
// overflow protection) are ignored — losing some claude output beats
// blocking the publisher.
func forwardRaw(sid string, sub *shell.Subscriber, out chan<- ptyChunk, stop <-chan struct{}) {
	for {
		select {
		case msg, ok := <-sub.C:
			if !ok {
				return
			}
			if msg.Drop || len(msg.Bytes) == 0 {
				continue
			}
			// Copy because the broadcaster reuses the slice across deliveries.
			cp := make([]byte, len(msg.Bytes))
			copy(cp, msg.Bytes)
			select {
			case out <- ptyChunk{sessionID: sid, data: cp}:
			case <-stop:
				return
			}
		case <-stop:
			return
		}
	}
}

// forwardSubscriber pumps events from a newly-attached session's
// subscriber onto the shared FanIn output channel until either the
// subscriber's channel closes (shell shut down) or stop closes (WS
// client disconnected).
func forwardSubscriber(sid string, sub *shell.EventSubscriber, out chan<- FanInEvent, stop <-chan struct{}) {
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			select {
			case out <- FanInEvent{SessionID: sid, Event: ev}:
			case <-stop:
				return
			}
		case <-stop:
			return
		}
	}
}

type namedRename struct {
	ID   string
	Name string
}

func handleInbound(msg InMsg, m *session.Manager, bridge *claude.Bridge, runner *claude.Runner, claudeEvents chan<- claudeEvtForward, runStates map[string]*claudeRunState, write func(OutMsg) error) {
	switch msg.Type {
	case "ping":
		_ = write(OutMsg{Type: "pong"})
	case "run":
		if msg.SessionID == "" {
			_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "run requires sessionID"})
			return
		}
		if len(msg.Command) == 0 {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "command is required"})
			return
		}
		if len(msg.Command) > maxCommandBytes {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "command_too_large", Message: "command exceeds 4096 bytes"})
			return
		}
		if errMsg := validateGitCommit(msg.Command); errMsg != "" {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "git_commit_needs_message", Message: errMsg})
			return
		}
		sh, err := m.Get(msg.SessionID)
		if errors.Is(err, session.ErrSessionNotFound) {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
			return
		}
		if err != nil {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
			return
		}
		cmdID := ulid.Make().String()
		_ = m.StoreFor().Save(msg.SessionID, store.Record{
			ID:        cmdID,
			SessionID: msg.SessionID,
			Command:   msg.Command,
			StartedAt: time.Now().UTC(),
			Status:    store.StatusRunning,
		})
		if err := sh.Write(cmdID, msg.Command); err != nil {
			// Roll the record back to interrupted — bash never started
			// this command. Without rollback the record would stay
			// "running" forever and the frontend would never see a done.
			if rec, gerr := m.StoreFor().Get(msg.SessionID, cmdID); gerr == nil {
				rec.Status = store.StatusInterrupted
				now := time.Now().UTC()
				rec.FinishedAt = &now
				_ = m.StoreFor().Save(msg.SessionID, rec)
			}
			switch {
			case errors.Is(err, shell.ErrBusy):
				_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "busy", Message: "shell is busy"})
			case errors.Is(err, shell.ErrUnavailable):
				_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unavailable", Message: "shell is unavailable"})
			default:
				_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "write_failed", Message: err.Error()})
			}
		}
	case "enter_claude":
		handleEnterClaude(msg, m, write)
	case "exit_claude":
		handleExitClaude(msg, m, write)
		// Interrupt any in-flight claude -p for this session.
		if st, ok := runStates[msg.SessionID]; ok && st.stop != nil {
			st.stop()
			delete(runStates, msg.SessionID)
		}
	case "stdin":
		handleStdin(msg, m, write)
	case "claude_prompt":
		handleClaudePrompt(msg, m, runner, claudeEvents, runStates, write)
	case "tool_decision":
		handleToolDecision(msg, bridge, write)
	case "interrupt":
		if st, ok := runStates[msg.SessionID]; ok && st.stop != nil {
			st.stop()
		}
	default:
		_ = write(OutMsg{Type: "error", Code: "bad_type", Message: "unknown message type"})
	}
}

func handleEnterClaude(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if msg.SessionID == "" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "enter_claude requires sessionID"})
		return
	}
	// Renderer selects between V0 TUI (xterm.js + raw PTY passthrough)
	// and V1 UI (React chat + claude -p stream-json). Empty defaults to
	// "tui" for backward compat with V0 clients that don't send the
	// field. New clients always send it.
	renderer := store.ClaudeRenderer(msg.Renderer)
	if renderer == "" {
		renderer = store.RendererTUI
	}
	if renderer != store.RendererTUI && renderer != store.RendererUI {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "renderer must be 'tui' or 'ui'"})
		return
	}
	if m.GetMode(msg.SessionID) == store.ModeClaude {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "already_in_claude", Message: "session is already in claude mode"})
		return
	}
	sh, err := m.Get(msg.SessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
		return
	}
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return
	}
	if sh.CurrentCommand() != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "session_busy", Message: "let the current command finish first"})
		return
	}

	// Ensure the per-session Claude conversation UUID exists. Both
	// renderers use --resume <uuid> so the dialogue persists across
	// renderer choices, Exit/re-enter, and Pod restart.
	if _, err := m.EnsureClaudeConvoID(msg.SessionID); err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return
	}

	switch renderer {
	case store.RendererTUI:
		// V0 path: send-keys `claude` into the tmux pane and let the
		// TUI take over the bytes that flow through pty_data.
		if err := sh.EnterClaude(); err != nil {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "enter_failed", Message: err.Error()})
			return
		}
	case store.RendererUI:
		// V1 path: do NOT touch the tmux pane. The pane stays at bash
		// prompt; we'll fork `claude -p ...` on demand from
		// handleClaudePrompt. The frontend will mount ClaudeChatView
		// and start sending claude_prompt frames.
		//
		// Make sure ~/.claude/settings.json points PreToolUse at our
		// bridge script so tool calls trigger the approval flow.
		// Idempotent — re-entering UI mode is cheap.
		home, herr := os.UserHomeDir()
		if herr == nil {
			if err := claude.EnsureSettingsHook(home); err != nil {
				slog.Warn("EnsureSettingsHook failed", "session", msg.SessionID, "err", err)
				// Non-fatal: claude will still run, but tool use will
				// be auto-approved instead of asking the user. We
				// surface a soft warning frame so the UI can show it.
				_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "settings_warning", Message: "could not configure PreToolUse hook: " + err.Error()})
			}
		}
	}

	if err := m.SetMode(msg.SessionID, store.ModeClaude); err != nil {
		slog.Warn("SetMode(claude) failed", "session", msg.SessionID, "err", err)
	}
	if err := m.SetRenderer(msg.SessionID, renderer); err != nil {
		slog.Warn("SetRenderer failed", "session", msg.SessionID, "err", err)
	}
	_ = write(OutMsg{Type: "claude_entered", SessionID: msg.SessionID, Renderer: string(renderer)})
}

func handleExitClaude(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if msg.SessionID == "" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "exit_claude requires sessionID"})
		return
	}
	if m.GetMode(msg.SessionID) != store.ModeClaude {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "not_in_claude", Message: "session is not in claude mode"})
		return
	}
	sh, err := m.Get(msg.SessionID)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
		return
	}
	// Dispatch the actual teardown by renderer.
	switch m.GetRenderer(msg.SessionID) {
	case store.RendererTUI, "":
		// V0 path: nudge claude in the pane to exit (it owns the
		// PTY). Empty renderer means a legacy V0 session — same
		// behavior.
		if err := sh.ExitClaude(); err != nil {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "exit_failed", Message: err.Error()})
			return
		}
	case store.RendererUI:
		// V1 path: there is no long-lived claude in the pane. If a
		// claude -p prompt is in flight, the WS handler's claude
		// runner is the one holding the process; ExitClaude here is
		// a no-op as far as the pane is concerned. Phase 3.4 will
		// also SIGINT the in-flight runner via its Stop().
	}
	if err := m.SetMode(msg.SessionID, store.ModeShell); err != nil {
		slog.Warn("SetMode(shell) failed", "session", msg.SessionID, "err", err)
	}
	if err := m.SetRenderer(msg.SessionID, ""); err != nil {
		slog.Warn("clear renderer failed", "session", msg.SessionID, "err", err)
	}
	_ = write(OutMsg{Type: "claude_exited", SessionID: msg.SessionID})
}

func handleStdin(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if msg.SessionID == "" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "stdin requires sessionID"})
		return
	}
	if m.GetMode(msg.SessionID) != store.ModeClaude {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "mode_mismatch", Message: "stdin is only valid in claude mode"})
		return
	}
	sh, err := m.Get(msg.SessionID)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
		return
	}
	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "stdin data must be base64"})
		return
	}
	if err := sh.SendStdin(data); err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "stdin_failed", Message: err.Error()})
		return
	}
}

// handleClaudePrompt forks `claude -p ...` for one user prompt and
// streams the parsed events back via claude_event frames. Only valid
// when the session is in claude mode with renderer=ui. Refuses if a
// prompt is already in flight (one at a time per session).
func handleClaudePrompt(msg InMsg, m *session.Manager, runner *claude.Runner, out chan<- claudeEvtForward, runStates map[string]*claudeRunState, write func(OutMsg) error) {
	if msg.SessionID == "" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "claude_prompt requires sessionID"})
		return
	}
	if strings.TrimSpace(msg.Text) == "" {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "prompt text required"})
		return
	}
	if m.GetMode(msg.SessionID) != store.ModeClaude {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "mode_mismatch", Message: "claude_prompt is only valid in claude mode"})
		return
	}
	if m.GetRenderer(msg.SessionID) != store.RendererUI {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "renderer_mismatch", Message: "claude_prompt requires renderer=ui"})
		return
	}
	if _, busy := runStates[msg.SessionID]; busy {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "busy", Message: "another prompt is still in flight"})
		return
	}
	convoID, err := m.EnsureClaudeConvoID(msg.SessionID)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return
	}
	// claude --resume requires running from the original cwd. We
	// don't track per-session cwds today (V0 sessions inherit /home/
	// alfred). For v1 we always invoke from /home/alfred — which is
	// also where claude wrote its first transcript, so --resume
	// works. If the user changed cwd inside a TUI claude before
	// switching to UI, this would mismatch; punted to v1.5.
	cwd := "/home/alfred"
	ctx, cancel := context.WithCancel(context.Background())
	pr, err := runner.Prompt(ctx, claude.PromptOptions{
		SessionUUID:    convoID,
		CWD:            cwd,
		Prompt:         msg.Text,
		PermissionMode: "default", // PreToolUse hook handles per-call asks
	})
	if err != nil {
		cancel()
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "claude_spawn_failed", Message: err.Error()})
		return
	}
	state := &claudeRunState{cancel: cancel, stop: pr.Stop}
	runStates[msg.SessionID] = state

	stopCh := make(chan struct{})
	// Forward parsed events as long as the runner emits.
	go func() {
		defer close(stopCh)
		for ev := range pr.Events {
			payload := claudeEventPayload(ev)
			select {
			case out <- claudeEvtForward{sessionID: msg.SessionID, kind: ev.Kind, payload: payload}:
			case <-ctx.Done():
				return
			}
		}
	}()
	// Reap the process exit + clean up.
	go func() {
		<-stopCh
		_ = pr.Wait()
		// Remove this run from in-flight map. Best-effort — the
		// state may have been cleared by exit_claude or interrupt.
		delete(runStates, msg.SessionID)
		cancel()
	}()
}

// handleToolDecision unblocks a PreToolUse hook waiting in the
// bridge. The toolUseID identifies which pending request to resolve.
func handleToolDecision(msg InMsg, bridge *claude.Bridge, write func(OutMsg) error) {
	if bridge == nil {
		_ = write(OutMsg{Type: "error", Code: "unavailable", Message: "claude bridge not configured"})
		return
	}
	if msg.ToolUseID == "" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "tool_decision requires toolUseId"})
		return
	}
	if msg.Decision != "allow" && msg.Decision != "deny" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "decision must be 'allow' or 'deny'"})
		return
	}
	if !bridge.Resolve(msg.ToolUseID, claude.Decision{
		Permission: msg.Decision,
		Reason:     msg.Reason,
	}) {
		// Pending request not found (timed out, already resolved).
		// Don't error to the client — race condition is benign;
		// nothing to do.
	}
}

func writeEventToClient(ev FanInEvent, write func(OutMsg) error) {
	switch {
	case ev.Event.Started != nil:
		s := ev.Event.Started
		_ = write(OutMsg{
			Type:      "started",
			SessionID: ev.SessionID,
			CmdID:     s.CmdID,
			Command:   s.Command,
			StartedAt: s.StartedAt.UTC().Format(time.RFC3339Nano),
		})
	case ev.Event.Chunk != nil:
		c := ev.Event.Chunk
		_ = write(OutMsg{
			Type:      "chunk",
			SessionID: ev.SessionID,
			CmdID:     c.CmdID,
			Data:      base64.StdEncoding.EncodeToString(c.Bytes),
		})
	case ev.Event.Ended != nil:
		// Persistence (WriteOutput + Record status/exit_code/finished_at) lives
		// in Manager.startPersister; that runs even with no WS client. Here we
		// only forward the "done" message to the connected client.
		e := ev.Event.Ended
		_ = write(OutMsg{
			Type:       "done",
			SessionID:  ev.SessionID,
			CmdID:      e.CmdID,
			ExitCode:   e.ExitCode,
			FinishedAt: e.FinishedAt.UTC().Format(time.RFC3339Nano),
		})
	}
}

var _ = context.Background
