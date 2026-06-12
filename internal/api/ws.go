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
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// maxCommandBytes caps the size of a single command sent via WS.
const maxCommandBytes = 4096

// maxInboundMessage caps the size of any WS frame from the client; protects
// against bloated frames.
const maxInboundMessage = 8 * 1024

// readDeadline is the max idle gap between client pings before we treat the
// connection as dead.
const readDeadline = 60 * time.Second

// pingInterval is the cadence of server-side WS pings.
const pingInterval = 20 * time.Second

var upgrader = websocket.Upgrader{
	// Same-origin only. Reject cross-origin upgrades. We compare Origin host
	// to Host header (TLS terminated upstream so scheme isn't decisive).
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Non-browser clients (test, curl-via-websocat) often have no Origin.
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

// Inbound message shape.
type inMsg struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
}

// Outbound message shape. Carries all variants in one struct; unset fields
// omitted from JSON.
type outMsg struct {
	Type        string `json:"type"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"` // base64-encoded chunk bytes
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

// WSHandler returns the /ws upgrade handler.
//
// Token validation runs BEFORE the upgrade so we never expose a half-baked
// connection to anyone who can't pass auth.
func WSHandler(sh *shell.Shell, st *store.Store, a auth.Auth) http.Handler {
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
		runClientLoop(conn, sh, st)
	})
}

// runClientLoop owns one WS connection until it dies.
func runClientLoop(conn *websocket.Conn, sh *shell.Shell, st *store.Store) {
	defer conn.Close()
	conn.SetReadLimit(maxInboundMessage)
	_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		return nil
	})

	// gorilla/websocket forbids concurrent writers — funnel all writes
	// through this mutex.
	var writeMu sync.Mutex
	write := func(m outMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(m)
	}

	// Subscribe to shell events BEFORE reading state to avoid losing events
	// fired in the gap between check and subscribe.
	sub, cancel := sh.SubscribeEvents(256)
	defer cancel()

	// Initial reattach or idle. CurrentCommand returns a snapshot of the
	// in-flight command with a copy of its buffer so far.
	if cur := sh.CurrentCommand(); cur != nil {
		_ = write(outMsg{
			Type:        "reattach",
			CmdID:       cur.ID,
			Command:     cur.Command,
			StartedAt:   cur.StartedAt.Format(time.RFC3339),
			OutputSoFar: base64.StdEncoding.EncodeToString(cur.Buffer),
		})
	} else {
		_ = write(outMsg{Type: "idle"})
	}

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	// Pump shell events to the client.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-sub.C:
				if !ok {
					return
				}
				switch {
				case evt.Started != nil:
					_ = write(outMsg{
						Type:      "started",
						CmdID:     evt.Started.CmdID,
						Command:   evt.Started.Command,
						StartedAt: evt.Started.StartedAt.Format(time.RFC3339),
					})
				case evt.Chunk != nil:
					_ = write(outMsg{
						Type:  "chunk",
						CmdID: evt.Chunk.CmdID,
						Data:  base64.StdEncoding.EncodeToString(evt.Chunk.Bytes),
					})
				case evt.Ended != nil:
					// Persist the output buffer and finalise the record.
					// Buffer ownership: shell.EndedEvent.Output is read-only
					// to us; we copy via WriteOutput.
					persistEnded(evt.Ended, st)
					_ = write(outMsg{
						Type:       "done",
						CmdID:      evt.Ended.CmdID,
						ExitCode:   evt.Ended.ExitCode,
						FinishedAt: evt.Ended.FinishedAt.Format(time.RFC3339),
					})
				}
			}
		}
	}()

	// Periodic ping to keep the connection alive across NAT timeouts.
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
			}
		}
	}()

	// Read loop: client → server.
	for {
		var msg inMsg
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		switch msg.Type {
		case "ping":
			_ = write(outMsg{Type: "pong"})
		case "run":
			handleRun(msg, sh, st, write)
		default:
			_ = write(outMsg{Type: "error", Code: "bad_request", Message: "unknown message type"})
		}
	}
}

func handleRun(msg inMsg, sh *shell.Shell, st *store.Store, write func(outMsg) error) {
	cmd := msg.Command
	if len(cmd) == 0 {
		_ = write(outMsg{Type: "error", Code: "bad_request", Message: "empty command"})
		return
	}
	if len(cmd) > maxCommandBytes {
		_ = write(outMsg{Type: "error", Code: "bad_request", Message: "command too long"})
		return
	}
	id := ulid.Make().String()
	now := time.Now().UTC()
	rec := store.Record{
		ID:        id,
		Command:   cmd,
		StartedAt: now,
		Status:    store.StatusRunning,
	}
	if err := st.Save("", rec); err != nil {
		_ = write(outMsg{Type: "error", Code: "store_error", Message: err.Error()})
		return
	}
	if err := sh.Write(id, cmd); err != nil {
		// Roll back the record so it doesn't sit forever as "running".
		rec.Status = store.StatusInterrupted
		_ = st.Save("", rec)
		code := "shell_error"
		switch {
		case errors.Is(err, shell.ErrBusy):
			code = "busy"
		case errors.Is(err, shell.ErrUnavailable):
			code = "shell_unavailable"
		}
		_ = write(outMsg{Type: "error", Code: code, Message: err.Error()})
	}
}

// persistEnded updates the store record + writes the output file for a
// command that just ended. Errors are logged, not surfaced — the WS layer
// has already done its job notifying the client; storage failures degrade
// gracefully (history may be incomplete but the live experience continues).
func persistEnded(evt *shell.EndedEvent, st *store.Store) {
	// Write output file first so a successful WriteOutput is reflected on
	// disk even if the metadata Save fails.
	if len(evt.Output) > 0 {
		if err := st.WriteOutput("", evt.CmdID, evt.Output); err != nil {
			slog.Error("store.WriteOutput on end", "cmdId", evt.CmdID, "err", err)
		}
	}
	rec, err := st.Get("", evt.CmdID)
	if err != nil {
		slog.Error("store.Get on end", "cmdId", evt.CmdID, "err", err)
		return
	}
	finishedAt := evt.FinishedAt
	rec.FinishedAt = &finishedAt
	ec := evt.ExitCode
	rec.ExitCode = &ec
	rec.OutputTruncated = evt.Truncated
	// ExitCode == -1 means bash died (interrupted by Stop or OOM). Other
	// non-zero codes are normal command failures.
	if evt.ExitCode == -1 {
		rec.Status = store.StatusInterrupted
	} else {
		rec.Status = store.StatusCompleted
	}
	if err := st.Save("", rec); err != nil {
		slog.Error("store.Save on end", "cmdId", evt.CmdID, "err", err)
	}
}
