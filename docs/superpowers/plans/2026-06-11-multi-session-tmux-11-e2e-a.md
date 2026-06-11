# Multi-session Plan 11 — E2E batch A (filesystem share, Go-restart survives, streaming chunks)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the three highest-value E2E scenarios from spec §9:
1. `TwoSessions_FilesystemShared`
2. `GoRestart_SessionsSurvive`
3. `GoRestart_DuringStreamingChunks`

**Architecture:** Existing `test/e2e/` framework (kind cluster, port-forward to 127.0.0.1:18080, login + WS). Add helper functions: `createSession`, `dialWSMulti` (wraps the multi-session protocol), `restartAlfredProcessInPod`. All three tests live in a new `e2e_multisession_test.go`.

**Tech Stack:** Go test framework + kind + `kubectl exec` for process restart.

**Spec sections covered:** §9 "Must pass to ship" scenarios 1, 2, 4.

---

## File Structure

```
test/e2e/
├── e2e_test.go                       # MODIFIED: helper additions
├── e2e_multisession_test.go          # NEW: the 3 scenarios
└── helpers_multisession.go           # NEW: helpers reused across plans 11-14
```

---

## Task 1: Multi-session helpers

**Files:**
- Create: `test/e2e/helpers_multisession.go`

- [ ] **Step 1: Implement helpers**

```go
//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsMsgMulti is the multi-session WS message shape.
type wsMsgMulti struct {
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

// createSession POSTs /api/sessions and returns the new session id.
func createSession(t *testing.T, token, name string) string {
	t.Helper()
	body := []byte("{}")
	if name != "" {
		body, _ = json.Marshal(map[string]string{"name": name})
	}
	req, _ := http.NewRequest("POST", baseHTTP+"/api/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", testIP(t))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		var msg map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		t.Fatalf("create session: code=%d body=%v", resp.StatusCode, msg)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.ID
}

// runInSession sends one command and waits for done; returns full output.
func runInSession(t *testing.T, conn *websocket.Conn, sessionID, command string, timeout time.Duration) (int, string) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sessionID, "command": command,
	}); err != nil {
		t.Fatalf("ws run: %v", err)
	}
	deadline := time.Now().Add(timeout)
	var buf bytes.Buffer
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m wsMsgMulti
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("ws read: %v", err)
		}
		if m.SessionID != "" && m.SessionID != sessionID {
			continue // not for us
		}
		if m.Type == "chunk" {
			data, _ := base64.StdEncoding.DecodeString(m.Data)
			buf.Write(data)
		}
		if m.Type == "done" {
			return m.ExitCode, buf.String()
		}
	}
	t.Fatalf("timeout waiting for done; collected so far: %q", buf.String())
	return -1, ""
}

// restartAlfredProcess SIGKILLs alfred-server inside the pod and waits for the
// container to respawn it. tini is PID 1 so the container stays alive.
func restartAlfredProcess(t *testing.T) {
	t.Helper()
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--",
		"sh", "-c", "pkill -KILL -f alfred-server || true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restart alfred: %v output=%s", err, out)
	}
	// Wait for the new process to bind :8080 (via port-forward to 18080).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseHTTP + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("alfred-server did not become ready within 30s after restart")
}

var _ = strings.TrimSpace
var _ = url.Parse
var _ = context.Background
var _ = fmt.Sprintf
```

- [ ] **Step 2: Commit**

```bash
git add test/e2e/helpers_multisession.go
git commit -m "test(e2e): helpers — createSession, runInSession, restartAlfredProcess"
```

---

## Task 2: TwoSessions_FilesystemShared

Two sessions write/read the same file path.

**Files:**
- Create: `test/e2e/e2e_multisession_test.go`

- [ ] **Step 1: Implement**

```go
//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
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
```

(Add the `github.com/gorilla/websocket` import at the top.)

- [ ] **Step 2: Run E2E**

Run: `make e2e-setup && make e2e -run TestE2E_TwoSessions_FilesystemShared`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_multisession_test.go
git commit -m "test(e2e): TwoSessions_FilesystemShared"
```

---

## Task 3: GoRestart_SessionsSurvive

Start a session, kick off `sleep 10 && echo done`, restart alfred-server, see the command finish and the session still present.

**Files:**
- Modify: `test/e2e/e2e_multisession_test.go`

- [ ] **Step 1: Implement**

Append:

```go
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
```

- [ ] **Step 2: Run**

Run: `make e2e -run TestE2E_GoRestart_SessionsSurvive`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_multisession_test.go
git commit -m "test(e2e): GoRestart_SessionsSurvive — process kill during a sleep, verify command completes"
```

---

## Task 4: GoRestart_DuringStreamingChunks

Run 100 lines of output streamed at 50ms each. Kill alfred mid-stream, verify the chat-stream eventually shows all 100 lines (offset resume works).

**Files:**
- Modify: `test/e2e/e2e_multisession_test.go`

- [ ] **Step 1: Implement**

Append:

```go
import "strings"

func TestE2E_GoRestart_DuringStreamingChunks(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "chunks")
	conn := dialWS(t, tok)
	drainStartupMessages(t, conn, time.Second)

	cmd := `for i in $(seq 1 100); do echo $i; sleep 0.05; done`
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": cmd}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForStarted(t, conn, sid, 5*time.Second)

	// Read for ~2.5 seconds, then kill alfred.
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m wsMsgMulti
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		_ = m
	}
	_ = conn.Close()
	restartAlfredProcess(t)

	// Re-attach, then poll the REST endpoint until the command shows up
	// with exit_code 0 and the output contains every integer 1..100.
	tok2, _ := login(t, testUser, testPassword)
	pollDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(pollDeadline) {
		req, _ := http.NewRequest("GET", baseHTTP+"/api/sessions/"+sid+"/commands", nil)
		req.Header.Set("Authorization", "Bearer "+tok2)
		req.Header.Set("X-Forwarded-For", testIP(t))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var list []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&list)
		resp.Body.Close()
		for _, c := range list {
			if status, _ := c["status"].(string); status == "completed" {
				// Fetch output.
				id := c["id"].(string)
				r2, _ := http.NewRequest("GET", baseHTTP+"/api/sessions/"+sid+"/commands/"+id, nil)
				r2.Header.Set("Authorization", "Bearer "+tok2)
				r2.Header.Set("X-Forwarded-For", testIP(t))
				resp2, _ := http.DefaultClient.Do(r2)
				var full map[string]any
				_ = json.NewDecoder(resp2.Body).Decode(&full)
				resp2.Body.Close()
				out, _ := full["output"].(string)
				// Verify every integer 1..100 is present.
				missing := 0
				for i := 1; i <= 100; i++ {
					if !strings.Contains(out, fmt.Sprintf("\n%d\n", i)) && !strings.HasPrefix(out, fmt.Sprintf("%d\n", i)) {
						missing++
					}
				}
				if missing > 0 {
					t.Fatalf("missing %d of 100 integers in output. output: %q", missing, out)
				}
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("command never completed after restart")
}
```

(Add `fmt`, `encoding/json`, `net/http` imports if not already there.)

- [ ] **Step 2: Run**

Run: `make e2e -run TestE2E_GoRestart_DuringStreamingChunks`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_multisession_test.go
git commit -m "test(e2e): GoRestart_DuringStreamingChunks — offset resume preserves every chunk"
```

---

## Plan 11 acceptance

- All three new E2E pass: `make e2e -run "TwoSessions_FilesystemShared|GoRestart_SessionsSurvive|GoRestart_DuringStreamingChunks"`.
- helpers_multisession.go is reusable for Plans 12/13/14.

## Plan 11 self-review checklist

- [ ] `restartAlfredProcess` polls `/healthz` (not arbitrary sleep) before returning.
- [ ] Each E2E test creates its own session and does not interfere with others.
- [ ] `drainStartupMessages` does not crash on a fresh session (no startup msgs).
