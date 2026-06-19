package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/oklog/ulid/v2"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/claudestate"
	"github.com/jesseliu/headless-alfred/internal/notes"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/store"
	"github.com/jesseliu/headless-alfred/internal/summary"
)

const (
	maxCommandBytes   = 4096
	maxInboundMessage = 8 * 1024
	readDeadline      = 60 * time.Second
	pingInterval      = 20 * time.Second
)

// errClientGone short-circuits all writes after the first WriteJSON
// failure on a connection — see guardedWriter. We don't surface this
// anywhere; it just stops downstream code from pushing more frames
// onto a dead socket.
var errClientGone = errors.New("ws: client gone")

// guardedWriter returns a serialized write closure that:
//   - serializes WriteJSON calls under a single mutex (gorilla
//     websocket.Conn forbids concurrent writes);
//   - on the FIRST write failure (TCP drop, half-close, client tab
//     closed), closes the conn so the reader goroutine's ReadJSON
//     wakes with an error; that closes inbound, which makes
//     runClientLoop's main select return, which runs the deferred
//     claudeRunStates.takeAll() + stopRun() to SIGINT any in-flight
//     `claude -p` and unblock its forwarder goroutine.
//
// Without this, a dead WS connection left runners blocked forever
// pushing onto the 64-slot claudeEvents channel that nobody was
// draining — the runner subprocess survived, kept burning the
// model's tokens, and the server's InFlight stayed true so the next
// client reconnect saw "Claude still thinking" forever.
func guardedWriter(writeJSON func(any) error, close func() error) func(OutMsg) error {
	var mu sync.Mutex
	var dead bool
	return func(msg OutMsg) error {
		mu.Lock()
		defer mu.Unlock()
		if dead {
			return errClientGone
		}
		if err := writeJSON(msg); err != nil {
			dead = true
			_ = close()
			return err
		}
		return nil
	}
}

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
// broadcaster is nil-safe: pass newRecapBroadcaster(nil) or a real
// broadcaster — the connection loop behaves correctly either way.
// csMgr may be nil — the dispatch helpers degrade to passthrough when nil.
func WSHandler(
	m *session.Manager,
	a auth.Auth,
	bridge *claude.Bridge,
	disp *claude.Dispatcher,
	broadcaster *recapBroadcaster,
	disk *diskBroadcaster,
	csMgr *claudestate.SessionManager,
) http.Handler {
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
		runClientLoop(conn, m, bridge, disp, runner, broadcaster, disk, csMgr)
	})
}

func runClientLoop(conn *websocket.Conn, m *session.Manager, bridge *claude.Bridge, disp *claude.Dispatcher, runner *claude.Runner, broadcaster *recapBroadcaster, disk *diskBroadcaster, csMgr *claudestate.SessionManager) {
	defer conn.Close()
	conn.SetReadLimit(maxInboundMessage)
	_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		return nil
	})

	write := guardedWriter(conn.WriteJSON, conn.Close)

	// Per-WS bg-task log subscriptions (fsnotify watchers).
	// Cleaned up on WS disconnect via bgLogSubs.closeAll() in the defer below.
	bgLogSubs := newBgTaskLogSubs()
	// connCtx is cancelled when the WS connection closes (via the stop channel).
	// Goroutines spawned by handleSubscribeBgTaskLog watch ctx.Done() so they
	// exit automatically on disconnect even without an explicit unsubscribe.
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()
	defer bgLogSubs.closeAll()

	// Resolvers for bg-task log subscriptions.
	bgMeta := NewSessionMetaResolver(m)
	bgCWD := NewSessionCWDResolver(m)

	sessions := m.ListAll()
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
		templateID := m.GetTemplateID(meta.ID)
		cur := sh.CurrentCommand()
		if cur == nil {
			_ = write(OutMsg{Type: "idle", SessionID: meta.ID, Mode: mode, Renderer: renderer, TemplateID: templateID})
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
				TemplateID:  templateID,
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
	claudeEvents := make(chan claudeEventEnvelope, 64)
	claudeRunStates := newClaudeRunStateMap()
	defer func() {
		// On disconnect, stop any still-running claude prompts so we
		// don't leak processes — AND apply a synthetic
		// claude_run_ended through server state. Without the Apply
		// step, the reaper goroutine's own take() would now return
		// owned=false (we already took the slot), so its run_ended
		// branch wouldn't fire — and server state would persist
		// InFlight=true / Done=false on the trailing turn forever.
		// The next reconnect's /claude-state hydrate would still
		// show "Claude is thinking…" with no way out.
		for _, e := range claudeRunStates.takeAll() {
			stopRun(e.state)
			applyClaudeRunEnded(e.sessionID, "client disconnected", csMgr)
		}
	}()

	// Per-WS fsnotify watcher on <DataDir>/summaries/. On a write
	// event, push a summary_updated frame for the matching session.
	// Failure to start is non-fatal: the sidebar stays stale until
	// the user navigates away and back, but the rest of the app
	// keeps working.
	summaryUpdates := make(chan string, 4)
	sw, swErr := summary.StartWatcher(m.DataDir(), func(sid string) {
		select {
		case summaryUpdates <- sid:
		case <-stop:
		}
	})
	if swErr != nil {
		slog.Warn("ws: summary watcher disabled", "err", swErr)
	} else {
		defer sw.Stop()
	}

	noteUpdates := make(chan string, 4)
	noteWatcher, noteErr := notes.StartWatcher(m.DataDir(), func(sid string) {
		select {
		case noteUpdates <- sid:
		case <-stop:
		}
	})
	if noteErr != nil {
		slog.Warn("notes watcher startup failed; notes UI will be stale", "err", noteErr)
	} else {
		defer noteWatcher.Stop()
	}

	// Per-connection subscription to the process-wide recap broadcaster.
	// Receives a date string each time a recap file is written.
	recapSub, recapUnsub := broadcaster.subscribe()
	defer recapUnsub()

	// Per-connection subscription to the disk-usage poller. Pushes
	// the current snapshot immediately (so the UI banner state is
	// correct without waiting one poll interval) and again whenever
	// the alert threshold flips.
	var diskSub <-chan DiskUsage
	if disk != nil {
		s, unsub := disk.subscribe()
		diskSub = s
		defer unsub()
	}

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
			handleInbound(msg, m, bridge, runner, claudeEvents, claudeRunStates, csMgr, write, connCtx, bgLogSubs, bgMeta, bgCWD)
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
			// invocation. Route through Apply so server state is the
			// source of truth, then push as a claude_event frame.
			cuid := m.GetClaudeSessionID(fwd.sessionID)
			dispatchClaudeStreamEvent(fwd, cuid, csMgr, write)
		case sid, ok := <-summaryUpdates:
			if !ok {
				continue
			}
			// Skip if the session was deleted between fsnotify
			// firing and us processing.
			if _, err := m.Get(sid); err != nil {
				continue
			}
			_ = write(OutMsg{Type: TypeSummaryUpdated, SessionID: sid})
		case sid, ok := <-noteUpdates:
			if !ok {
				continue
			}
			if _, err := m.Get(sid); err != nil {
				continue
			}
			_ = write(OutMsg{Type: TypeNoteUpdated, SessionID: sid})
		case date, ok := <-recapSub:
			if !ok {
				return
			}
			_ = write(OutMsg{Type: TypeRecapUpdated, Date: date})
		case du, ok := <-diskSub:
			if !ok {
				// Broadcaster closed our channel (Close called or
				// resubscribed elsewhere). Drop ref so the select
				// stops firing this case on every iteration.
				diskSub = nil
				continue
			}
			duCopy := du
			_ = write(OutMsg{Type: TypeDiskUsage, DiskUsage: &duCopy})
		case sid := <-closedCh:
			// Session was deleted via REST or by another tab. Kill any
			// in-flight `claude -p` we were holding for it AND apply a
			// run_ended through server state — the session is going
			// away but other tabs may still be looking at it during
			// the brief delete-broadcast window.
			if st, ok := claudeRunStates.take(sid); ok {
				stopRun(st)
				applyClaudeRunEnded(sid, "session closed", csMgr)
			}
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

// requireSessionID guards handlers whose only common precondition is
// a non-empty SessionID. Returns false and writes a bad_request error
// when missing, so the caller can early-return.
func requireSessionID(msg InMsg, frameType string, write func(OutMsg) error) bool {
	if msg.SessionID != "" {
		return true
	}
	_ = write(OutMsg{Type: "error", Code: "bad_request", Message: frameType + " requires sessionID"})
	return false
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

func handleInbound(
	msg InMsg,
	m *session.Manager,
	bridge *claude.Bridge,
	runner *claude.Runner,
	claudeEvents chan<- claudeEventEnvelope,
	runStates *claudeRunStateMap,
	csMgr *claudestate.SessionManager,
	write func(OutMsg) error,
	connCtx context.Context,
	bgLogSubs *bgTaskLogSubs,
	bgMeta MetaResolver,
	bgCWD CWDResolver,
) {
	switch msg.Type {
	case "ping":
		_ = write(OutMsg{Type: "pong"})
	case "run":
		handleRun(msg, m, write)
	case "enter_claude":
		handleEnterClaude(msg, m, write)
	case "exit_claude":
		handleExitClaude(msg, m, write)
		// Interrupt any in-flight claude -p for this session AND finalize
		// the trailing turn in server state — otherwise InFlight would
		// stay true after the user explicitly left claude mode.
		if st, ok := runStates.take(msg.SessionID); ok {
			stopRun(st)
			applyClaudeRunEnded(msg.SessionID, "exited claude mode", csMgr)
		}
	case "stdin":
		handleStdin(msg, m, write)
	case "claude_prompt":
		handleClaudePrompt(msg, m, runner, claudeEvents, runStates, csMgr, write)
	case "tool_decision":
		handleToolDecision(msg, bridge, write)
		dispatchToolDecision(msg.SessionID, msg.ToolUseID, msg.Decision, msg.Reason, csMgr, write)
	case "interrupt":
		// Don't take() — keep the state in the map so the reaper
		// goroutine can clean up after the SIGINT'd process exits.
		if st, ok := runStates.get(msg.SessionID); ok && st.stop != nil {
			st.stop()
		}
	case "subscribe_bg_task_log":
		handleSubscribeBgTaskLog(connCtx, SubscribeBgTaskLogPayload{TaskID: msg.TaskID}, bgLogSubs, write, msg.SessionID, bgMeta, bgCWD)
	case "unsubscribe_bg_task_log":
		handleUnsubscribeBgTaskLog(UnsubscribeBgTaskLogPayload{TaskID: msg.TaskID}, bgLogSubs)
	default:
		_ = write(OutMsg{Type: "error", Code: "bad_type", Message: "unknown message type"})
	}
}

func handleRun(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if !requireSessionID(msg, "run", write) {
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

// dispatchClaudeStreamEvent routes one Claude stream-json event
// through the SessionManager's Apply so server state is the truth
// source, then emits a claude_event frame to the client. The
// server's Apply-time wall clock travels INSIDE the Payload (as
// the Timestamp field of claudestate.Event) — the frontend reducer
// reads it from there into state fields like StartedAt.
//
// claudeUUID is passed in so the first GetOrLoad can attach a jsonl
// path; subsequent calls for the same session ignore it.
func dispatchClaudeStreamEvent(env claudeEventEnvelope, claudeUUID string, mgr *claudestate.SessionManager, write func(OutMsg) error) {
	// Defensive: tests can pass nil to assert the legacy passthrough.
	// In production main.go wires a non-nil manager.
	if mgr == nil {
		_ = write(OutMsg{
			Type:      "claude_event",
			SessionID: env.sessionID,
			EventKind: string(env.kind),
			Payload:   env.payload,
		})
		return
	}
	st, err := mgr.GetOrLoad(env.sessionID, claudeUUID)
	if err != nil {
		slog.Warn("ws: get state for stream event", "sid", env.sessionID, "err", err)
		_ = write(OutMsg{Type: "claude_event", SessionID: env.sessionID, EventKind: string(env.kind), Payload: env.payload})
		return
	}
	ev, err := buildEventFromEnvelope(env)
	if err != nil {
		slog.Warn("ws: build event", "sid", env.sessionID, "err", err)
		_ = write(OutMsg{Type: "claude_event", SessionID: env.sessionID, EventKind: string(env.kind), Payload: env.payload})
		return
	}
	if err := st.Apply(ev); err != nil {
		slog.Warn("ws: apply event", "sid", env.sessionID, "kind", env.kind, "err", err)
	}
	// Re-marshal payload to a stable RawMessage. The server-side
	// timestamp travels in the top-level Timestamp field so the
	// frontend reducer can read it directly without diving into the
	// payload variant.
	out, _ := json.Marshal(ev.Payload)
	_ = write(OutMsg{
		Type:      "claude_event",
		SessionID: env.sessionID,
		EventKind: string(env.kind),
		Payload:   json.RawMessage(out),
		Timestamp: ev.Timestamp.Format(time.RFC3339Nano),
	})
}

// dispatchToolDecision applies the user's tool decision to in-memory
// state and emits a tool_decision_applied frame. The bridge resolution
// (telling the PreToolUse hook to allow or deny) still happens through
// the existing bridge path — this helper only owns the state side.
func dispatchToolDecision(sessionID, toolUseID, decision, reason string, mgr *claudestate.SessionManager, write func(OutMsg) error) {
	if mgr == nil {
		return
	}
	st, err := mgr.GetOrLoad(sessionID, "")
	if err != nil {
		slog.Warn("ws: get state for decision", "sid", sessionID, "err", err)
		return
	}
	ev := claudestate.Event{
		Kind:      claudestate.EventToolDecision,
		Timestamp: time.Now().UTC(),
		Payload: &claudestate.ToolDecisionPayload{
			ToolUseID: toolUseID,
			Decision:  decision,
			Reason:    reason,
		},
	}
	if err := st.Apply(ev); err != nil {
		slog.Warn("ws: apply decision", "sid", sessionID, "err", err)
		return
	}
	_ = write(OutMsg{
		Type:      "tool_decision_applied",
		SessionID: sessionID,
		ToolUseID: toolUseID,
		Decision:  decision,
		Timestamp: ev.Timestamp.Format(time.RFC3339Nano),
	})
}

// applyClaudeRunEnded routes a ClaudeRunEnded through Apply so the
// trailing turn is finalized in server state (Done=true, IsError=
// true, InFlight=false). Returns the Apply timestamp so the caller
// can stamp it onto an outgoing claude_run_ended frame and have
// streaming state match post-refresh hydrate state byte-for-byte.
// Safe to call with a nil manager (no-op).
//
// Used by:
//   - the reaper goroutine on runner exit (pairs with a frame write
//     since a live client may still be reading)
//   - runClientLoop's disconnect defer when SIGINT'ing leaked runners
//     (no frame — there's no client to read it; we just need server
//     state to stop saying "thinking…" forever)
func applyClaudeRunEnded(sessionID, message string, mgr *claudestate.SessionManager) time.Time {
	now := time.Now().UTC()
	if mgr == nil {
		return now
	}
	st, err := mgr.GetOrLoad(sessionID, "")
	if err != nil {
		slog.Warn("ws: get state for claude_run_ended", "sid", sessionID, "err", err)
		return now
	}
	ev := claudestate.Event{
		Kind:      claudestate.EventClaudeRunEnded,
		Timestamp: now,
		Payload:   &claudestate.ClaudeRunEndedPayload{Message: message},
	}
	if applyErr := st.Apply(ev); applyErr != nil {
		slog.Warn("ws: apply claude_run_ended", "sid", sessionID, "err", applyErr)
	}
	return now
}

// dispatchClaudeRunEnded is the reaper-side helper: Apply + emit the
// claude_run_ended frame to the client. Use it whenever the runner
// dies and we still hold a writable conn.
func dispatchClaudeRunEnded(sessionID, message string, mgr *claudestate.SessionManager, write func(OutMsg) error) {
	ts := applyClaudeRunEnded(sessionID, message, mgr)
	_ = write(OutMsg{
		Type:      "claude_run_ended",
		SessionID: sessionID,
		Message:   message,
		Timestamp: ts.Format(time.RFC3339Nano),
	})
}

// dispatchClaudeError applies a ClaudeError event to server state and
// emits a claude_error frame. Used when the server itself decides a
// run is over (spawn failure, write-to-dead-client, shutdown, session
// deletion) — paths where the runner won't emit a result or run_ended
// of its own. The Apply call finalizes the in-flight turn so the
// composer unlocks, then the frame fan-outs to the originating client.
// Safe to call with a nil manager (no-op + frame still flies).
func dispatchClaudeError(sessionID, code, message string, mgr *claudestate.SessionManager, write func(OutMsg) error) {
	now := time.Now().UTC()
	if mgr != nil {
		if st, err := mgr.GetOrLoad(sessionID, ""); err == nil {
			ev := claudestate.Event{
				Kind:      claudestate.EventClaudeError,
				Timestamp: now,
				Payload: &claudestate.ClaudeErrorPayload{
					Code:    code,
					Message: message,
				},
			}
			if applyErr := st.Apply(ev); applyErr != nil {
				slog.Warn("ws: apply claude_error", "sid", sessionID, "err", applyErr)
			}
		} else {
			slog.Warn("ws: get state for claude_error", "sid", sessionID, "err", err)
		}
	}
	_ = write(OutMsg{
		Type:      "claude_error",
		SessionID: sessionID,
		Code:      code,
		Message:   message,
		Timestamp: now.Format(time.RFC3339Nano),
	})
}

// dispatchClaudePromptBegin creates a server-side turn id, registers
// the turn via BeginTurn on the SessionManager, and emits the
// turn_started frame so the frontend's optimistic placeholder can be
// reconciled. Returns the new turn id (or "" on failure).
//
// Use it from handleClaudePrompt AFTER the final prompt text has been
// composed (template-rendered, summary-suffixed, etc.) so the turn's
// `prompt` field reflects what the user will actually see in the
// "You" bubble.
func dispatchClaudePromptBegin(sessionID, clientNonce, prompt string, mgr *claudestate.SessionManager, write func(OutMsg) error) string {
	if mgr == nil {
		return ""
	}
	st, err := mgr.GetOrLoad(sessionID, "")
	if err != nil {
		slog.Warn("ws: get state for prompt", "sid", sessionID, "err", err)
		return ""
	}
	turnID := ulid.Make().String()
	now := time.Now().UTC()
	st.BeginTurn(turnID, prompt, now)
	_ = write(OutMsg{
		Type:        "turn_started",
		SessionID:   sessionID,
		ClientNonce: clientNonce,
		TurnID:      turnID,
		Timestamp:   now.Format(time.RFC3339Nano),
	})
	return turnID
}

// buildEventFromEnvelope turns a raw envelope (kind string + payload)
// into a typed claudestate.Event. The timestamp is generated here —
// it's the server's Apply-time moment.
func buildEventFromEnvelope(env claudeEventEnvelope) (claudestate.Event, error) {
	now := time.Now().UTC()
	// Marshal the raw payload (which is either json.RawMessage or a
	// concrete payload struct from the parser) then unmarshal into a
	// claudestate.Event so the kind-dispatched UnmarshalJSON allocates
	// the right concrete payload type.
	payloadBytes, err := json.Marshal(env.payload)
	if err != nil {
		return claudestate.Event{}, err
	}
	wire := struct {
		Kind      claudestate.EventKind `json:"kind"`
		Timestamp time.Time             `json:"timestamp"`
		Payload   json.RawMessage       `json:"payload"`
	}{
		Kind:      claudestate.EventKind(env.kind),
		Timestamp: now,
		Payload:   payloadBytes,
	}
	envBytes, err := json.Marshal(wire)
	if err != nil {
		return claudestate.Event{}, err
	}
	var ev claudestate.Event
	if err := json.Unmarshal(envBytes, &ev); err != nil {
		return claudestate.Event{}, err
	}
	return ev, nil
}
