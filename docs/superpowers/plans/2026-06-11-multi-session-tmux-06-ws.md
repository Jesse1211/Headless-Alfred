# Multi-session Plan 6 — WS protocol with sessionID

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite `internal/api/ws.go` so a single WS connection serves all N sessions. Every inbound `run` carries `sessionID`; every outbound `started`/`chunk`/`done`/`reattach`/`idle` carries `sessionID`. Add new `session_closed` / `session_renamed` broadcasts wired to `session.Manager`'s listeners.

**Architecture:** One WS goroutine per client. On connect, subscribe to every existing session's `EventBroadcaster` AND set the Manager's close/rename listeners (additively — multiple WS clients may be connected). When the client sends `{type:'run', sessionID, command}`, look the session up via `manager.Get(sid)` and call `Write` on it. The session-close listener pushes `session_closed` to all WS clients; cleanup is handled when the underlying broadcaster closes the subscriber channel.

**Tech Stack:** gorilla/websocket (unchanged) + Plan 4's `session.Manager`.

**Spec sections covered:** §6.2 (WS protocol shape).

---

## File Structure

```
internal/api/
├── ws.go               # REWRITE in place
├── ws_test.go          # REWRITE (existing tests use shell.Shell directly)
└── ws_fanin.go         # NEW: helper that multiplexes N session event channels into one
```

Splitting `ws_fanin.go` keeps the per-connection loop in `ws.go`
focused on protocol decoding; the fan-in is mechanical.

---

## Task 1: Fan-in helper (multiplex N session event channels)

A small standalone helper that takes N `(sessionID, *EventSubscriber)`
pairs and returns a single channel of `(sessionID, CommandEvent)`.

**Files:**
- Create: `internal/api/ws_fanin.go`
- Create: `internal/api/ws_fanin_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/api/ws_fanin_test.go`:

```go
package api

import (
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/shell"
)

func TestFanin_DeliversFromMultipleSubs(t *testing.T) {
	bcastA := shell.NewEventBroadcaster()
	bcastB := shell.NewEventBroadcaster()
	subA := bcastA.Subscribe(8)
	subB := bcastB.Subscribe(8)

	out := make(chan FanInEvent, 16)
	stop := make(chan struct{})
	go FanIn([]NamedSubscriber{
		{SessionID: "A", Sub: subA},
		{SessionID: "B", Sub: subB},
	}, out, stop)

	bcastA.Publish(shell.CommandEvent{Started: &shell.StartedEvent{CmdID: "1"}})
	bcastB.Publish(shell.CommandEvent{Started: &shell.StartedEvent{CmdID: "2"}})

	gotA, gotB := false, false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !(gotA && gotB) {
		select {
		case ev := <-out:
			if ev.SessionID == "A" && ev.Event.Started.CmdID == "1" {
				gotA = true
			}
			if ev.SessionID == "B" && ev.Event.Started.CmdID == "2" {
				gotB = true
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !gotA || !gotB {
		t.Fatalf("missing: A=%v B=%v", gotA, gotB)
	}
	close(stop)
}

func TestFanin_StopReturnsCleanly(t *testing.T) {
	bcast := shell.NewEventBroadcaster()
	sub := bcast.Subscribe(8)
	out := make(chan FanInEvent, 4)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		FanIn([]NamedSubscriber{{SessionID: "A", Sub: sub}}, out, stop)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FanIn did not return after stop closed")
	}
}
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/api/ -run TestFanin -count=1`
Expected: BUILD FAILS on `FanIn`, `NamedSubscriber`, `FanInEvent` undefined.

- [ ] **Step 3: Implement fan-in**

Create `internal/api/ws_fanin.go`:

```go
package api

import (
	"reflect"

	"github.com/jesseliu/headless-alfred/internal/shell"
)

// FanInEvent is one delivery: which session it came from, and the event.
type FanInEvent struct {
	SessionID string
	Event     shell.CommandEvent
}

// NamedSubscriber pairs a sessionID with the subscriber that delivers
// its events.
type NamedSubscriber struct {
	SessionID string
	Sub       *shell.EventSubscriber
}

// FanIn multiplexes events from N subscribers onto out. It returns when
// stop is closed; partial delivery does not occur (any in-flight read
// is delivered before returning).
//
// We use reflect.Select because the number of subscribers is dynamic.
// A typical alfred Pod has <=8 sessions so the small constant overhead
// is fine.
func FanIn(subs []NamedSubscriber, out chan<- FanInEvent, stop <-chan struct{}) {
	cases := make([]reflect.SelectCase, 0, len(subs)+1)
	cases = append(cases, reflect.SelectCase{
		Dir:  reflect.SelectRecv,
		Chan: reflect.ValueOf(stop),
	})
	for _, s := range subs {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(s.Sub.C),
		})
	}
	for {
		idx, v, ok := reflect.Select(cases)
		if idx == 0 {
			return // stop closed
		}
		if !ok {
			// One subscriber's channel closed (e.g., shell.Shell shut down).
			// Remove it from cases and continue. Cases at indices > 0
			// correspond to subs[idx-1]; rebuild without that one.
			subs = append(subs[:idx-1], subs[idx:]...)
			cases = append(cases[:idx], cases[idx+1:]...)
			if len(subs) == 0 {
				return
			}
			continue
		}
		ev := v.Interface().(shell.CommandEvent)
		select {
		case out <- FanInEvent{SessionID: subs[idx-1].SessionID, Event: ev}:
		case <-stop:
			return
		}
	}
}
```

- [ ] **Step 4: Run, confirm green**

Run: `go test ./internal/api/ -run TestFanin -race -count=1 -v`
Expected: 2 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/ws_fanin.go internal/api/ws_fanin_test.go
git commit -m "api: ws_fanin helper to multiplex N session event channels"
```

---

## Task 2: Rewrite WSHandler + protocol with sessionID

The big rewrite. Keep the function name `WSHandler` so router.go doesn't
change yet. New signature: `WSHandler(m *session.Manager, a auth.Auth)`.
Plan 7 (boot) drops the legacy `Shell` and `Store` fields from `Deps`.

**Files:**
- Replace: `internal/api/ws.go`
- Replace: `internal/api/ws_test.go`
- Modify: `internal/api/router.go` (1 line: signature change)

- [ ] **Step 1: Write the new test framework**

Replace `internal/api/ws_test.go` with:

```go
package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialWS(t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	dialURL := strings.Replace(url, "http://", "ws://", 1) + "?token=" + token
	h := http.Header{}
	c, _, err := websocket.DefaultDialer.Dial(dialURL, h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func setupWSServerMulti(t *testing.T) (string, *session.Manager) {
	t.Helper()
	m := newTestManager(t)
	a := auth.New("admin", "pw", "tok")
	rl := auth.NewRateLimiter(5, time.Minute)
	r := NewRouter(Deps{
		Manager:     m,
		Auth:        a,
		RateLimiter: rl,
		Ready:       func() bool { return true },
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL, m
}

func TestWS_OnConnect_SendsIdleForEverySession(t *testing.T) {
	url, m := setupWSServerMulti(t)
	a, _ := m.Create("A")
	b, _ := m.Create("B")
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	// Expect 2 idle messages, one per session.
	seen := map[string]bool{}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(seen) < 2 {
		_ = conn.SetReadDeadline(deadline)
		var msg outMsg
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		if msg.Type == "idle" {
			seen[msg.SessionID] = true
		}
	}
	if !seen[a.ID] || !seen[b.ID] {
		t.Fatalf("missing idle: %+v", seen)
	}
}

func TestWS_RunWithUnknownSessionID_ReturnsError(t *testing.T) {
	url, _ := setupWSServerMulti(t)
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	// Drain any startup idle frames (none, since no sessions).
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	for {
		var m outMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
	}
	_ = conn.WriteJSON(inMsg{Type: "run", SessionID: "nope", Command: "ls"})
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg outMsg
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg.Type != "error" || msg.Code != "unknown_session" {
		t.Fatalf("expected error/unknown_session, got %+v", msg)
	}
}

func TestWS_SessionClosed_BroadcastsToConnectedClients(t *testing.T) {
	url, m := setupWSServerMulti(t)
	sess, _ := m.Create("A")
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	// Drain the startup idle.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for i := 0; i < 5; i++ {
		var m2 outMsg
		if err := conn.ReadJSON(&m2); err != nil {
			break
		}
		if m2.Type == "idle" && m2.SessionID == sess.ID {
			break
		}
	}
	_ = m.Close(sess.ID)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var m2 outMsg
		if err := conn.ReadJSON(&m2); err != nil {
			t.Fatalf("never received session_closed: %v", err)
		}
		if m2.Type == "session_closed" && m2.SessionID == sess.ID {
			return
		}
	}
}

func TestWS_SessionRenamed_BroadcastsToConnectedClients(t *testing.T) {
	url, m := setupWSServerMulti(t)
	sess, _ := m.Create("A")
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for i := 0; i < 5; i++ {
		var m2 outMsg
		if err := conn.ReadJSON(&m2); err != nil {
			break
		}
		if m2.Type == "idle" {
			break
		}
	}
	_ = m.Rename(sess.ID, "training")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var m2 outMsg
		if err := conn.ReadJSON(&m2); err != nil {
			t.Fatalf("never received session_renamed: %v", err)
		}
		if m2.Type == "session_renamed" && m2.SessionID == sess.ID && m2.Name == "training" {
			return
		}
	}
}

var _ = base64.StdEncoding // silence unused import (used elsewhere when chunks arrive)
var _ = json.Marshal
```

Add these imports:

```go
import (
	// add to existing:
	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/session"
)
```

- [ ] **Step 2: Run, confirm failures (signatures + fields)**

Run: `go test ./internal/api/ -run TestWS -count=1`
Expected: BUILD FAILS on `inMsg.SessionID`, `outMsg.SessionID`, `outMsg.Name`, `Deps.Manager` (already added in Plan 5), and changes in WSHandler signature.

- [ ] **Step 3: Rewrite ws.go**

Replace `internal/api/ws.go` (preserves the helper constants and the
upgrader; rewires the loop):

```go
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

// WSHandler upgrades the connection and runs one loop per client.
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

	// 1. Subscribe to every existing session's events.
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

	// 2. Initial idle/reattach per session.
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

	// 3. Register listeners for session_closed / session_renamed.
	closedCh := make(chan string, 4)
	renamedCh := make(chan namedRename, 4)
	m.SetCloseListener(func(sid string) {
		select {
		case closedCh <- sid:
		default:
		}
	})
	m.SetRenameListener(func(sid, name string) {
		select {
		case renamedCh <- namedRename{ID: sid, Name: name}:
		default:
		}
	})
	defer m.SetCloseListener(nil)
	defer m.SetRenameListener(nil)

	// 4. Start fan-in + ping ticker + read pump.
	events := make(chan FanInEvent, 64)
	stop := make(chan struct{})
	defer close(stop)
	go FanIn(subs, events, stop)
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	// Reader goroutine: pumps inbound messages onto a channel.
	inbound := make(chan inMsg, 4)
	go func() {
		for {
			var msg inMsg
			if err := conn.ReadJSON(&msg); err != nil {
				close(inbound)
				return
			}
			inbound <- msg
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
			writeEventToClient(ev, write, m)
		case sid := <-closedCh:
			_ = write(outMsg{Type: "session_closed", SessionID: sid})
		case rn := <-renamedCh:
			_ = write(outMsg{Type: "session_renamed", SessionID: rn.ID, Name: rn.Name})
		}
	}
}

type namedRename struct {
	ID   string
	Name string
}

// handleInbound routes a single client message.
func handleInbound(msg inMsg, m *session.Manager, write func(outMsg) error) {
	switch msg.Type {
	case "ping":
		_ = write(outMsg{Type: "pong"})
	case "run":
		if msg.SessionID == "" {
			_ = write(outMsg{Type: "error", Code: "bad_request", Message: "run requires sessionID"})
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
		// Persist immediately as running.
		_ = m.StoreFor().Save(msg.SessionID, store.Record{
			ID:        cmdID,
			SessionID: msg.SessionID,
			Command:   msg.Command,
			StartedAt: time.Now().UTC(),
			Status:    store.StatusRunning,
		})
		if err := sh.Write(cmdID, msg.Command); err != nil {
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

// writeEventToClient translates a CommandEvent into an outMsg and writes it.
// Also persists the Ended event's output buffer to disk (the disk-writer
// responsibility lives here for simplicity in the multi-session world).
func writeEventToClient(ev FanInEvent, write func(outMsg) error, m *session.Manager) {
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
		e := ev.Event.Ended
		// Persist final output + update record to completed.
		_ = m.StoreFor().WriteOutput(ev.SessionID, e.CmdID, e.Output)
		if rec, err := m.StoreFor().Get(ev.SessionID, e.CmdID); err == nil {
			rec.ExitCode = ptrInt(e.ExitCode)
			rec.FinishedAt = ptrTime(e.FinishedAt)
			rec.OutputTruncated = e.Truncated
			if e.ExitCode == 0 {
				rec.Status = store.StatusCompleted
			} else {
				rec.Status = store.StatusStopped
			}
			_ = m.StoreFor().Save(ev.SessionID, rec)
		}
		_ = write(outMsg{
			Type:       "done",
			SessionID:  ev.SessionID,
			CmdID:      e.CmdID,
			ExitCode:   e.ExitCode,
			FinishedAt: e.FinishedAt.UTC().Format(time.RFC3339Nano),
		})
	}
}

func ptrInt(v int) *int       { return &v }
func ptrTime(t time.Time) *time.Time { return &t }

// Keep context import alive (used by chi router elsewhere; ensures
// this file's imports stay consistent if someone refactors).
var _ = context.Background
```

- [ ] **Step 4: Update router.go to use the new WSHandler signature**

In `internal/api/router.go`, change the `r.Get("/ws", ...)` line:

```go
	r.Get("/ws", WSHandler(d.Manager, d.Auth).ServeHTTP)
```

And drop the legacy `Shell` / `Store` fields from `Deps`:

```go
type Deps struct {
	Manager     *session.Manager
	Auth        auth.Auth
	RateLimiter *auth.RateLimiter
	Ready       func() bool
}
```

Remove the `internal/shell` and `internal/store` imports from
router.go if they were only there for those fields.

- [ ] **Step 5: Run, confirm green**

Run: `go test ./internal/api/ -race -count=1 -v`
Expected: All WS and sessions/commands tests PASS.

If `cmd/alfred-server` fails to compile here, **leave it broken**; Plan 7 wires it.

- [ ] **Step 6: Commit**

```bash
git add internal/api/ws.go internal/api/ws_test.go internal/api/router.go
git commit -m "api: WS protocol — every message carries sessionID; session_closed/renamed broadcasts; fan-in across N sessions"
```

---

## Plan 6 acceptance

- `go test -race ./internal/api/ -count=1` is green.
- `cmd/alfred-server` does NOT compile yet — Plan 7 fixes boot wiring.
- WS protocol matches spec §6.2.
- `session_closed` and `session_renamed` broadcasts flow to all connected clients.

---

## Plan 6 self-review checklist

- [ ] No `TODO|FIXME|XXX` in `internal/api/ws.go` or `internal/api/ws_fanin.go`.
- [ ] `outMsg.SessionID` is the json tag matching the frontend client (Plan 8 will assert exactly `sessionID` camelCase).
- [ ] The fan-in loop closes cleanly on stop (covered by `TestFanin_StopReturnsCleanly`).
- [ ] `disk-writer` responsibility (Save with Status update + WriteOutput) lives in `writeEventToClient`; no separate disk goroutine.
