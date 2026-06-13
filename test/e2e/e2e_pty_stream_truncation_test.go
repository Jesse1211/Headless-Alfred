//go:build e2e

package e2e

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jesseliu/headless-alfred/internal/api"
)

// Plan 3 defines `shell.StreamTruncateThreshold = 8 MiB`. Two 6 MiB
// commands plus a small middle command guarantee the threshold is
// crossed between commands and that the truncate fires at the next
// idle boundary — exactly the racy path spec §4.4 describes.
const sixMiB = 6 * 1024 * 1024

func TestE2E_PtyStream_Truncation_NoLostBytes(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "truncation")
	conn := dialWS(t, tok)

	// Command 1: 6 MiB of output.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid,
		"command": "yes y | head -c " + strconv.Itoa(sixMiB),
	}); err != nil {
		t.Fatalf("write c1: %v", err)
	}
	cmd1ID := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd1ID, 60*time.Second)

	// Command 2: tiny — this is where the stream truncation can fire.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "echo MIDDLE",
	}); err != nil {
		t.Fatalf("write c2: %v", err)
	}
	cmd2ID := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd2ID, 10*time.Second)

	// Command 3: another 6 MiB.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid,
		"command": "yes y | head -c " + strconv.Itoa(sixMiB),
	}); err != nil {
		t.Fatalf("write c3: %v", err)
	}
	cmd3ID := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd3ID, 60*time.Second)

	// Give the persister goroutine a moment to flush each record (Status,
	// Output) to disk before we read it back. Done events arrive on the WS
	// before WriteOutput + Save complete in Manager.startPersister.
	waitForPersisted(t, tok, sid, cmd3ID, 5*time.Second)

	// Fetch each command's persisted output.
	for label, id := range map[string]string{"cmd1": cmd1ID, "cmd2": cmd2ID, "cmd3": cmd3ID} {
		body := getJSON(t, tok, "/api/sessions/"+sid+"/commands/"+id)
		var full map[string]any
		_ = json.Unmarshal(body, &full)
		out, _ := full["output"].(string)
		switch label {
		case "cmd1", "cmd3":
			if len(out) < sixMiB {
				t.Fatalf("%s output len = %d, want >= %d", label, len(out), sixMiB)
			}
			// Verify it is all 'y\n' patterned (no garbage spliced in).
			if strings.Count(out, "y") < sixMiB/2 {
				t.Fatalf("%s output suspicious: only %d 'y' chars in %d bytes",
					label, strings.Count(out, "y"), len(out))
			}
		case "cmd2":
			if !strings.Contains(out, "MIDDLE") {
				t.Fatalf("cmd2 lost its MIDDLE: %q", out)
			}
		}
	}
}

func waitForStartedReturnID(t *testing.T, conn *websocket.Conn, sessionID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("ws: %v", err)
		}
		if m.SessionID == sessionID && m.Type == "started" {
			return m.CmdID
		}
	}
	t.Fatal("no started")
	return ""
}

func waitForDone(t *testing.T, conn *websocket.Conn, sessionID, cmdID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("ws read: %v", err)
		}
		if m.SessionID == sessionID && m.Type == "done" && m.CmdID == cmdID {
			return
		}
	}
	t.Fatalf("never saw done for %s", cmdID)
}

// waitForPersisted polls the REST API until the given command record has
// status=completed (or the timeout elapses). The persister goroutine in
// Manager.startPersister runs independently of the WS write of "done", so
// without a poll a test that reads the store immediately after done can
// observe status=running and an empty output.
func waitForPersisted(t *testing.T, tok, sid, cmdID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body := getJSON(t, tok, "/api/sessions/"+sid+"/commands/"+cmdID)
		var rec map[string]any
		_ = json.Unmarshal(body, &rec)
		if s, _ := rec["status"].(string); s == "completed" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("command %s never persisted within %s", cmdID, timeout)
}
