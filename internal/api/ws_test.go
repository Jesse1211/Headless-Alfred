//go:build !windows

package api

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/store"
)

func requireBashAndShell(t *testing.T) (*shell.Shell, *store.Store, *auth.Auth) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	sh := shell.NewShell(slog.Default())
	if err := sh.Start(); err != nil {
		t.Fatalf("shell start: %v", err)
	}
	t.Cleanup(func() { _ = sh.Close() })
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &auth.Auth{Token: "TOK"}
	return sh, st, a
}

func setupWSServer(t *testing.T) (string, *shell.Shell, *store.Store) {
	t.Helper()
	sh, st, a := requireBashAndShell(t)
	h := WSHandler(sh, st, *a)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	u.Scheme = "ws"
	u.RawQuery = "token=TOK"
	return u.String(), sh, st
}

type wsMsg struct {
	Type        string `json:"type"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

func dial(t *testing.T, u string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.DialContext(context.Background(), u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func read(t *testing.T, c *websocket.Conn, timeout time.Duration) wsMsg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	var m wsMsg
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	if err := c.WriteJSON(v); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestWS_IdleOnConnect(t *testing.T) {
	u, _, _ := setupWSServer(t)
	c := dial(t, u)
	m := read(t, c, 3*time.Second)
	if m.Type != "idle" {
		t.Fatalf("type = %q, want idle", m.Type)
	}
}

func TestWS_RejectsBadToken(t *testing.T) {
	u, _, _ := setupWSServer(t)
	bad := strings.Replace(u, "token=TOK", "token=WRONG", 1)
	_, resp, err := websocket.DefaultDialer.DialContext(context.Background(), bad, nil)
	if err == nil {
		t.Fatal("expected dial to fail")
	}
	if resp != nil && resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWS_RunSimpleCommand(t *testing.T) {
	u, _, st := setupWSServer(t)
	c := dial(t, u)
	_ = read(t, c, 3*time.Second) // idle

	send(t, c, map[string]string{"type": "run", "command": "echo hello-ws"})
	var (
		gotStarted, gotChunk, gotDone bool
		cmdID                         string
		body                          strings.Builder
	)
	deadline := time.Now().Add(10 * time.Second)
	for !gotDone && time.Now().Before(deadline) {
		m := read(t, c, 5*time.Second)
		switch m.Type {
		case "started":
			gotStarted = true
			cmdID = m.CmdID
		case "chunk":
			gotChunk = true
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			body.Write(b)
		case "done":
			gotDone = true
			if m.CmdID != cmdID {
				t.Fatalf("done.cmdId = %q, want %q", m.CmdID, cmdID)
			}
			if m.ExitCode != 0 {
				t.Fatalf("exit = %d, want 0", m.ExitCode)
			}
		}
	}
	if !gotStarted || !gotChunk || !gotDone {
		t.Fatalf("missed events: started=%v chunk=%v done=%v", gotStarted, gotChunk, gotDone)
	}
	if !strings.Contains(body.String(), "hello-ws") {
		t.Fatalf("body=%q", body.String())
	}
	// Verify store has the completed record AND the output was persisted.
	rec, err := st.Get(cmdID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if rec.Status != store.StatusCompleted {
		t.Fatalf("status = %s, want completed", rec.Status)
	}
	out, _ := st.ReadOutput(cmdID)
	if !strings.Contains(string(out), "hello-ws") {
		t.Fatalf("persisted output = %q, want it to contain hello-ws", out)
	}
}

func TestWS_BusyError(t *testing.T) {
	u, _, _ := setupWSServer(t)
	c := dial(t, u)
	_ = read(t, c, 3*time.Second) // idle
	send(t, c, map[string]string{"type": "run", "command": "sleep 1"})
	// First started.
	for {
		m := read(t, c, 3*time.Second)
		if m.Type == "started" {
			break
		}
	}
	send(t, c, map[string]string{"type": "run", "command": "echo nope"})
	// Find the error message.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m := read(t, c, 2*time.Second)
		if m.Type == "error" && m.Code == "busy" {
			return
		}
	}
	t.Fatal("never received busy error")
}

func TestWS_ReattachAfterReconnect(t *testing.T) {
	u, _, _ := setupWSServer(t)
	c1 := dial(t, u)
	_ = read(t, c1, 3*time.Second) // idle

	send(t, c1, map[string]string{"type": "run", "command": "sleep 1; echo done-r"})
	for {
		m := read(t, c1, 3*time.Second)
		if m.Type == "started" {
			break
		}
	}
	_ = c1.Close()
	time.Sleep(200 * time.Millisecond)

	c2 := dial(t, u)
	m := read(t, c2, 5*time.Second)
	if m.Type != "reattach" {
		t.Fatalf("type = %q, want reattach. msg=%+v", m.Type, m)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m := read(t, c2, 5*time.Second)
		if m.Type == "done" {
			if m.ExitCode != 0 {
				t.Fatalf("exit = %d", m.ExitCode)
			}
			return
		}
	}
	t.Fatal("no done event")
}

func TestWS_EmptyCommandRejected(t *testing.T) {
	u, _, _ := setupWSServer(t)
	c := dial(t, u)
	_ = read(t, c, 3*time.Second) // idle
	send(t, c, map[string]string{"type": "run", "command": ""})
	m := read(t, c, 3*time.Second)
	if m.Type != "error" || m.Code != "bad_request" {
		t.Fatalf("got %+v, want error/bad_request", m)
	}
}
