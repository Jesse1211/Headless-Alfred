# Multi-session Plan 12 — E2E `Reconcile_StoredButNotLive_RebuildsSession`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One E2E scenario, end-to-end. Simulates "Pod restart, tmux server lost" by killing the tmux server inside the pod. Reconcile must rebuild a fresh tmux session for each entry in `sessions.json`, mark any in-flight commands as `interrupted`, and the API must still list the **old** completed commands from the store.

**Architecture:** Uses helpers from Plan 11 (`createSession`, `restartAlfredProcess`). New helper: `killTmuxServerInPod`. The test runs against the existing kind cluster.

**Tech Stack:** Go E2E + kubectl exec.

**Spec sections covered:** §4.7 reconciliation, branch 2 (`stored \ live`).

---

## File Structure

```
test/e2e/
├── helpers_multisession.go              # MODIFY: add killTmuxServerInPod
└── e2e_reconcile_stored_not_live_test.go  # NEW
```

---

## Task 1: killTmuxServerInPod helper

**Files:**
- Modify: `test/e2e/helpers_multisession.go`

- [ ] **Step 1: Append helper**

Add to `helpers_multisession.go`:

```go
// killTmuxServerInPod terminates the tmux server inside the alfred pod,
// leaving the alfred-server process alive and the per-session command
// JSONs untouched on the PVC. Used to simulate "container survived,
// tmux died" — which is functionally the same as the §4.7 "stored \
// live" reconciliation branch from alfred-server's perspective.
func killTmuxServerInPod(t *testing.T) {
	t.Helper()
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--",
		"sh", "-c", "pkill -KILL tmux || true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kill tmux in pod: %v output=%s", err, out)
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add test/e2e/helpers_multisession.go
git commit -m "test(e2e): killTmuxServerInPod helper"
```

---

## Task 2: The scenario

**Files:**
- Create: `test/e2e/e2e_reconcile_stored_not_live_test.go`

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

func TestE2E_Reconcile_StoredButNotLive_RebuildsSession(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	if tok == "" {
		t.Fatal("login")
	}
	sid := createSession(t, tok, "rebuild-target")
	conn := dialWS(t, tok)
	drainStartupMessages(t, conn, time.Second)

	// Establish some completed command history so the rebuild can be
	// observed as "session reappears with history intact".
	if exit, _ := runInSession(t, conn, sid, "echo PRE_RESTART", 5*time.Second); exit != 0 {
		t.Fatalf("pre-restart command failed: exit=%d", exit)
	}
	// Kick off a long-running command that WILL be interrupted by tmux dying.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "sleep 60",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForStarted(t, conn, sid, 5*time.Second)
	_ = conn.Close()

	// Kill tmux. alfred-server is now talking to a dead socket; the next
	// HTTP call (or its next tmux invocation) will fail and trigger the
	// recovery path. We don't restart alfred-server itself — the
	// reconcile-on-recover behaviour fires on next process boot.
	killTmuxServerInPod(t)
	// Force alfred-server to restart so reconcile runs.
	restartAlfredProcess(t)

	tok2, _ := login(t, testUser, testPassword)

	// 1. Session is still listed.
	sessionsList := getJSON(t, tok2, "/api/sessions")
	var found bool
	var raw []map[string]any
	_ = json.Unmarshal(sessionsList, &raw)
	for _, s := range raw {
		if s["id"] == sid {
			found = true
		}
	}
	if !found {
		t.Fatalf("session %s missing after reconcile. list: %s", sid, sessionsList)
	}

	// 2. The completed PRE_RESTART command is still in the store.
	commandsList := getJSON(t, tok2, "/api/sessions/"+sid+"/commands")
	if !strings.Contains(string(commandsList), "echo PRE_RESTART") {
		t.Fatalf("PRE_RESTART command not found after reconcile. list: %s", commandsList)
	}

	// 3. The in-flight `sleep 60` is now marked interrupted.
	var cmds []map[string]any
	_ = json.Unmarshal(commandsList, &cmds)
	sawInterrupted := false
	for _, c := range cmds {
		if c["command"] == "sleep 60" && c["status"] == "interrupted" {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Fatalf("sleep 60 was not marked interrupted. cmds: %s", commandsList)
	}

	// 4. We can immediately use the session with a new bash (proves rebuild).
	conn2 := dialWS(t, tok2)
	drainStartupMessages(t, conn2, time.Second)
	exit, out := runInSession(t, conn2, sid, "echo POST_RESTART", 5*time.Second)
	if exit != 0 || !strings.Contains(out, "POST_RESTART") {
		t.Fatalf("post-restart command failed: exit=%d out=%q", exit, out)
	}
}

// getJSON is a small auth-GET helper.
func getJSON(t *testing.T, tok, path string) []byte {
	t.Helper()
	req, _ := http.NewRequest("GET", baseHTTP+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: code=%d", path, resp.StatusCode)
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf
}
```

- [ ] **Step 2: Run**

Run: `make e2e -run TestE2E_Reconcile_StoredButNotLive_RebuildsSession`
Expected: PASS.

If `sleep 60` is marked `completed` instead of `interrupted`, the reconciliation in `Manager.Reconcile()` is not calling `SweepRunningToInterrupted` after rebuilding the session — go back to Plan 4 Task 4 and verify.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_reconcile_stored_not_live_test.go
git commit -m "test(e2e): Reconcile_StoredButNotLive_RebuildsSession"
```

---

## Plan 12 acceptance

- `make e2e -run TestE2E_Reconcile_StoredButNotLive_RebuildsSession` PASSes.
- `killTmuxServerInPod` does not affect other E2E tests (they each create fresh sessions).

## Plan 12 self-review checklist

- [ ] Test creates its own session (no fixed-name reuse across runs).
- [ ] All four post-reconcile assertions are independent and would fail loudly on regression.
- [ ] `getJSON` is small enough not to need its own test file.
