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

// persistTimeout bounds how long we wait for a command's record to reach
// status=completed on the REST side. We deliberately do NOT gate this test on
// the live WS event stream: that stream is lossy BY DESIGN — EventBroadcaster
// drops Started/Ended frames for a slow subscriber (see CONTEXT.md), and
// pushing 6 MiB over the kubectl port-forward back-pressures the socket until
// the `done` frame is dropped, not merely delayed. So we drive each command
// over the WS but synchronize on the authoritative REST record instead, which
// the persister writes independently of live-frame delivery. 60s is generous
// for a 6 MiB record flush on a CI kind node.
const persistTimeout = 60 * time.Second // CI kind is slower than local

func TestE2E_PtyStream_Truncation_NoLostBytes(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "truncation")
	conn := dialWS(t, tok)

	// Command 1: 6 MiB. Send + sync on the REST record, never the WS frame.
	cmd1ID := runAndWaitPersisted(t, conn, tok, sid,
		"yes y | head -c "+strconv.Itoa(sixMiB))

	// Command 2: tiny — this is where the stream truncation can fire.
	cmd2ID := runAndWaitPersisted(t, conn, tok, sid, "echo MIDDLE")

	// Command 3: another 6 MiB (crosses the cumulative 8 MiB truncate boundary).
	cmd3ID := runAndWaitPersisted(t, conn, tok, sid,
		"yes y | head -c "+strconv.Itoa(sixMiB))

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

// runAndWaitPersisted sends a command over the WS, then synchronizes purely on
// the REST record — never on the lossy live event stream. It discovers the new
// command's id by polling /commands for an id not seen before this call, then
// waits for that record to reach status=completed. This is immune to dropped
// Started/Ended frames (which a multi-MiB stream over kubectl port-forward
// provokes by design) while still driving the real command through the backend.
func runAndWaitPersisted(t *testing.T, conn *websocket.Conn, tok, sid, command string) string {
	t.Helper()
	before := map[string]bool{}
	for _, id := range listCommandIDs(t, tok, sid) {
		before[id] = true
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": command,
	}); err != nil {
		t.Fatalf("write %q: %v", command, err)
	}
	// Discover the new command id from REST (not the WS started frame).
	var cmdID string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && cmdID == "" {
		for _, id := range listCommandIDs(t, tok, sid) {
			if !before[id] {
				cmdID = id
				break
			}
		}
		if cmdID == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if cmdID == "" {
		t.Fatalf("new command never appeared in REST list for %q", command)
	}
	waitForPersisted(t, tok, sid, cmdID, persistTimeout)
	return cmdID
}

// listCommandIDs returns the ids in /api/sessions/{sid}/commands.
func listCommandIDs(t *testing.T, tok, sid string) []string {
	t.Helper()
	body := getJSON(t, tok, "/api/sessions/"+sid+"/commands")
	var list []map[string]any
	_ = json.Unmarshal(body, &list)
	ids := make([]string, 0, len(list))
	for _, c := range list {
		if id, ok := c["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
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
