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

type inMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID,omitempty"`
	Command   string `json:"command,omitempty"`
}

type outMsg struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionID,omitempty"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Name        string `json:"name,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
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
	write := func(msg outMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(msg)
	}

	sessions := m.List()
	subs := make([]NamedSubscriber, 0, len(sessions))
	cancels := []func(){}
	for _, meta := range sessions {
		sh, err := m.Get(meta.ID)
		if err != nil {
			continue
		}
		sub, cancel := sh.SubscribeEvents(16)
		subs = append(subs, NamedSubscriber{SessionID: meta.ID, Sub: sub})
		cancels = append(cancels, cancel)
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
		cur := sh.CurrentCommand()
		if cur == nil {
			_ = write(outMsg{Type: "idle", SessionID: meta.ID})
		} else {
			_ = write(outMsg{
				Type:        "reattach",
				SessionID:   meta.ID,
				CmdID:       cur.ID,
				Command:     cur.Command,
				StartedAt:   cur.StartedAt.UTC().Format(time.RFC3339Nano),
				OutputSoFar: base64.StdEncoding.EncodeToString(cur.Buffer),
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
	stop := make(chan struct{})
	defer close(stop)
	go FanIn(subs, events, stop)
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	inbound := make(chan inMsg, 4)
	go func() {
		for {
			var msg inMsg
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
			writeEventToClient(ev, write)
		case sid := <-closedCh:
			_ = write(outMsg{Type: "session_closed", SessionID: sid})
		case rn := <-renamedCh:
			_ = write(outMsg{Type: "session_renamed", SessionID: rn.ID, Name: rn.Name})
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
			_ = write(outMsg{Type: "idle", SessionID: sid})
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

func handleInbound(msg inMsg, m *session.Manager, write func(outMsg) error) {
	switch msg.Type {
	case "ping":
		_ = write(outMsg{Type: "pong"})
	case "run":
		if msg.SessionID == "" {
			_ = write(outMsg{Type: "error", Code: "bad_request", Message: "run requires sessionID"})
			return
		}
		if len(msg.Command) == 0 {
			_ = write(outMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "command is required"})
			return
		}
		if len(msg.Command) > maxCommandBytes {
			_ = write(outMsg{Type: "error", SessionID: msg.SessionID, Code: "command_too_large", Message: "command exceeds 4096 bytes"})
			return
		}
		sh, err := m.Get(msg.SessionID)
		if errors.Is(err, session.ErrSessionNotFound) {
			_ = write(outMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
			return
		}
		if err != nil {
			_ = write(outMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
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
				_ = write(outMsg{Type: "error", SessionID: msg.SessionID, Code: "busy", Message: "shell is busy"})
			case errors.Is(err, shell.ErrUnavailable):
				_ = write(outMsg{Type: "error", SessionID: msg.SessionID, Code: "unavailable", Message: "shell is unavailable"})
			default:
				_ = write(outMsg{Type: "error", SessionID: msg.SessionID, Code: "write_failed", Message: err.Error()})
			}
		}
	default:
		_ = write(outMsg{Type: "error", Code: "bad_type", Message: "unknown message type"})
	}
}

func writeEventToClient(ev FanInEvent, write func(outMsg) error) {
	switch {
	case ev.Event.Started != nil:
		s := ev.Event.Started
		_ = write(outMsg{
			Type:      "started",
			SessionID: ev.SessionID,
			CmdID:     s.CmdID,
			Command:   s.Command,
			StartedAt: s.StartedAt.UTC().Format(time.RFC3339Nano),
		})
	case ev.Event.Chunk != nil:
		c := ev.Event.Chunk
		_ = write(outMsg{
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
		_ = write(outMsg{
			Type:       "done",
			SessionID:  ev.SessionID,
			CmdID:      e.CmdID,
			ExitCode:   e.ExitCode,
			FinishedAt: e.FinishedAt.UTC().Format(time.RFC3339Nano),
		})
	}
}

var _ = context.Background
