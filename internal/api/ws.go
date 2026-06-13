package api

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/oklog/ulid/v2"

	"github.com/jesseliu/headless-alfred/internal/auth"
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

func WSHandler(m *session.Manager, a auth.Auth) http.Handler {
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
		runClientLoop(conn, m)
	})
}

func runClientLoop(conn *websocket.Conn, m *session.Manager) {
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
		cur := sh.CurrentCommand()
		if cur == nil {
			_ = write(OutMsg{Type: "idle", SessionID: meta.ID, Mode: mode})
		} else {
			_ = write(OutMsg{
				Type:        "reattach",
				SessionID:   meta.ID,
				CmdID:       cur.ID,
				Command:     cur.Command,
				StartedAt:   cur.StartedAt.UTC().Format(time.RFC3339Nano),
				OutputSoFar: base64.StdEncoding.EncodeToString(cur.Buffer),
				Mode:        mode,
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
			handleInbound(msg, m, write)
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

func handleInbound(msg InMsg, m *session.Manager, write func(OutMsg) error) {
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
	case "stdin":
		handleStdin(msg, m, write)
	default:
		_ = write(OutMsg{Type: "error", Code: "bad_type", Message: "unknown message type"})
	}
}

func handleEnterClaude(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if msg.SessionID == "" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "enter_claude requires sessionID"})
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
	if err := sh.EnterClaude(); err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "enter_failed", Message: err.Error()})
		return
	}
	// Flip mode. Any error here is non-fatal for the user-visible state
	// (bash already received `claude\n`); we log and proceed.
	if err := m.SetMode(msg.SessionID, store.ModeClaude); err != nil {
		slog.Warn("SetMode(claude) failed", "session", msg.SessionID, "err", err)
	}
	_ = write(OutMsg{Type: "claude_entered", SessionID: msg.SessionID})
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
	// Try to nudge claude to exit by sending Ctrl+C twice. We do NOT
	// SIGKILL — claude may have an in-flight tool call. The UI warned
	// the user.
	if err := sh.ExitClaude(); err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "exit_failed", Message: err.Error()})
		return
	}
	// Flip mode immediately — the frontend wants to switch view now.
	// If claude is stubborn and stays alive, the user will see ChatStream
	// with junk in the next idle frame; they can re-enter claude or
	// click Stop on the bash level.
	if err := m.SetMode(msg.SessionID, store.ModeShell); err != nil {
		slog.Warn("SetMode(shell) failed", "session", msg.SessionID, "err", err)
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
