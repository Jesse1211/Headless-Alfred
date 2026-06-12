//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestE2E_TwoSessions_FilesystemShared(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	if tok == "" {
		t.Fatal("login failed")
	}
	a := createSession(t, tok, "A")
	b := createSession(t, tok, "B")

	conn := dialWS(t, tok)
	drainStartupMessages(t, conn, time.Second)

	dir := "/tmp/alfred-fs-test-" + a[:6]
	code, _ := runInSession(t, conn, a, "mkdir -p "+dir+" && touch "+dir+"/shared && echo done", 5*time.Second)
	if code != 0 {
		t.Fatalf("mkdir in A exit=%d", code)
	}
	code2, out := runInSession(t, conn, b, "ls "+dir, 5*time.Second)
	if code2 != 0 {
		t.Fatalf("ls in B exit=%d output=%q", code2, out)
	}
	if !strings.Contains(out, "shared") {
		t.Fatalf("B did not see A's file. ls output: %q", out)
	}
}

func drainStartupMessages(t *testing.T, conn *websocket.Conn, until time.Duration) {
	t.Helper()
	deadline := time.Now().Add(until)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m wsMsgMulti
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		_ = m
	}
}

func TestE2E_GoRestart_SessionsSurvive(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "long")
	conn := dialWS(t, tok)
	drainStartupMessages(t, conn, time.Second)

	// Kick off the long command and wait for started.
	cmd := "sleep 8 && echo HELLO_AFTER_RESTART"
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": cmd}); err != nil {
		t.Fatalf("write run: %v", err)
	}
	waitForStarted(t, conn, sid, 5*time.Second)
	_ = conn.Close()

	// Restart alfred-server while bash continues running in tmux.
	restartAlfredProcess(t)

	// Re-login (process is new), reconnect, drain reattach.
	tok2, _ := login(t, testUser, testPassword)
	conn2 := dialWS(t, tok2)
	// Wait for the EVENTUAL done event for our session.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn2.SetReadDeadline(deadline)
		var m wsMsgMulti
		if err := conn2.ReadJSON(&m); err != nil {
			t.Fatalf("read after restart: %v", err)
		}
		if m.SessionID == sid && m.Type == "done" {
			if m.ExitCode != 0 {
				t.Fatalf("exit=%d", m.ExitCode)
			}
			return
		}
	}
	t.Fatal("never saw done for the surviving command")
}

func waitForStarted(t *testing.T, conn *websocket.Conn, sessionID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m wsMsgMulti
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("ws read: %v", err)
		}
		if m.SessionID == sessionID && m.Type == "started" {
			return
		}
	}
	t.Fatal("no started")
}
