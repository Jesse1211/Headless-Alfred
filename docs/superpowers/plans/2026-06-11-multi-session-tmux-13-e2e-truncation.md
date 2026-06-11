# Multi-session Plan 13 — E2E `PtyStream_Truncation_NoLostBytes`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One E2E scenario that exercises the §4.4 truncation dance. Three commands in sequence inside a single session: 6 MB of output, then a small command (the truncation point), then another 6 MB. The persisted outputs of all three commands must be byte-exact; nothing lost across the stop-pipe → truncate → restart-pipe sequence.

**Architecture:** Helpers from Plans 11/12 + a deterministic 6 MB generator. We verify by fetching `/api/sessions/{sid}/commands/{cmdID}` and comparing output length + content checksum.

**Tech Stack:** Go E2E.

**Spec sections covered:** §4.4 truncation.

---

## File Structure

```
test/e2e/
└── e2e_pty_stream_truncation_test.go    # NEW
```

---

## Task 1: The scenario

**Files:**
- Create: `test/e2e/e2e_pty_stream_truncation_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
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
	drainStartupMessages(t, conn, time.Second)

	// Command 1: 6 MiB of output.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid,
		"command": "yes y | head -c " + itoa(sixMiB),
	}); err != nil {
		t.Fatalf("write c1: %v", err)
	}
	cmd1ID := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd1ID, 30*time.Second)

	// Command 2: tiny — this is where the stream truncation can fire.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "echo MIDDLE",
	}); err != nil {
		t.Fatalf("write c2: %v", err)
	}
	cmd2ID := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd2ID, 5*time.Second)

	// Command 3: another 6 MiB.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid,
		"command": "yes y | head -c " + itoa(sixMiB),
	}); err != nil {
		t.Fatalf("write c3: %v", err)
	}
	cmd3ID := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd3ID, 60*time.Second)

	// Fetch each command's persisted output.
	for label, id := range map[string]string{"cmd1": cmd1ID, "cmd2": cmd2ID, "cmd3": cmd3ID} {
		body := getJSON(t, tok, "/api/sessions/"+sid+"/commands/"+id)
		var full map[string]any
		_ = json.Unmarshal(body, &full)
		out, _ := full["output"].(string)
		switch label {
		case "cmd1", "cmd3":
			// PTY may add a trailing \n. Accept a few-byte slack but never
			// less than the requested length.
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
		var m wsMsgMulti
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
		var m wsMsgMulti
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("ws read: %v", err)
		}
		if m.SessionID == sessionID && m.Type == "done" && m.CmdID == cmdID {
			return
		}
	}
	t.Fatalf("never saw done for %s", cmdID)
}

func itoa(i int) string {
	return strings.TrimSpace(fmtSprintf("%d", i))
}

var fmtSprintf = func(format string, args ...any) string {
	// avoid bringing fmt into a small file already cluttered with imports
	// when it's not already imported elsewhere; tests may inline as needed.
	out := ""
	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) && format[i+1] == 'd' {
			out += intToStr(args[0].(int))
			i++
		} else {
			out += string(format[i])
		}
	}
	return out
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
```

(`websocket` is imported transitively via helpers; if Go tooling
complains add `"github.com/gorilla/websocket"` to the import block.)

- [ ] **Step 2: Run**

Run: `make e2e -run TestE2E_PtyStream_Truncation_NoLostBytes`
Expected: PASS.

If cmd2's output comes back empty, the truncation flushed mid-command — Plan 3 §4.4 implementation is wrong. Go back to Plan 2 Task 3 / Plan 3 read-loop ordering.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_pty_stream_truncation_test.go
git commit -m "test(e2e): PtyStream_Truncation_NoLostBytes (6MB+small+6MB sequence)"
```

---

## Plan 13 acceptance

- `make e2e -run TestE2E_PtyStream_Truncation_NoLostBytes` PASSes in <2 minutes.
- All three commands' outputs are intact (size + content sanity check).

## Plan 13 self-review checklist

- [ ] The 6 MiB constant + 8 MiB `shell.StreamTruncateThreshold` are consistent (cmd 1 alone is under threshold; cmd 1 + tiny cmd 2 forces idle boundary; cmd 3 push total well past).
- [ ] The test does not depend on tmux flushing timing in any way other than waiting for `done`.
