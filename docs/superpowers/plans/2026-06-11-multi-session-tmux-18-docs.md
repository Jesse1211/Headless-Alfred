# Multi-session Plan 18 — Recommended E2E + docs + CI + Dockerfile

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the 5 recommended (non-blocking) E2E scenarios, update README + CONTEXT.md to reflect the multi-session architecture, install `tmux` in the runtime Dockerfile and in CI, and update the GitHub Actions workflow. After this plan: green main on push, multi-session is shipped.

**Architecture:** 5 small E2E test files (one scenario each), three docs updates, two ops file updates (Dockerfile + CI yaml).

**Tech Stack:** Go E2E, Markdown, Dockerfile, GitHub Actions YAML.

**Spec sections covered:** §9 "Recommended" scenarios; §10 (deferred items); §11 (out of scope) — updated in CONTEXT.md.

---

## File Structure

```
test/e2e/
├── e2e_reconcile_live_not_stored_test.go     # NEW
├── e2e_bash_exit_test.go                     # NEW
├── e2e_stop_respawn_test.go                  # NEW
├── e2e_migration_test.go                     # NEW
└── e2e_rename_persists_test.go               # NEW
Dockerfile                                    # MODIFY: install tmux
.github/workflows/ci.yaml                     # MODIFY: install tmux in jobs
README.md                                     # MODIFY: multi-session intro
CONTEXT.md                                    # MODIFY: new traps + invariants
```

---

## Task 1: Install tmux in Dockerfile

**Files:**
- Modify: `Dockerfile`

- [ ] **Step 1: Update Dockerfile**

In the runtime stage's `apt-get install` line, add `tmux`:

```dockerfile
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates tini tmux \
 && rm -rf /var/lib/apt/lists/* \
 ...
```

- [ ] **Step 2: Rebuild image locally to verify**

```bash
./scripts/build-image.sh
```

Expected: builds without error; tmux is in the image.

- [ ] **Step 3: Commit**

```bash
git add Dockerfile
git commit -m "docker: install tmux in runtime image (required for multi-session)"
```

---

## Task 2: Install tmux in CI workflow

**Files:**
- Modify: `.github/workflows/ci.yaml`

- [ ] **Step 1: Add a step to each job that needs tmux**

In the `Go unit + integration` job, before `Run tests`:

```yaml
      - name: Install tmux
        run: sudo apt-get update && sudo apt-get install -y tmux
```

In the `E2E in kind` job: tmux runs inside the pod via the Dockerfile, so no host install is needed.

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/ci.yaml
git commit -m "ci: install tmux on Go test runner (integration tests need it on PATH)"
```

---

## Task 3-5: Recommended E2E scenarios

Each one is its own file + one commit.

### Task 3: Reconcile_LiveButNotStored_KillsOrphan

**Files:**
- Create: `test/e2e/e2e_reconcile_live_not_stored_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestE2E_Reconcile_LiveButNotStored_KillsOrphan(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	// Create an orphan tmux session directly via the socket. alfred-server
	// holds the socket at /data/alfred-tmux.sock.
	out := execInPod(t, "tmux -S /data/alfred-tmux.sock new-session -d -s ghost-orphan bash --noprofile --norc")
	_ = out
	// Restart alfred-server so Reconcile runs.
	restartAlfredProcess(t)
	// The orphan must be gone.
	listing := execInPod(t, "tmux -S /data/alfred-tmux.sock ls 2>&1 || true")
	if strings.Contains(listing, "ghost-orphan") {
		t.Fatalf("orphan tmux session still alive after reconcile: %s", listing)
	}
	// And no leaked entry in /api/sessions.
	apiList := getJSON(t, tok, "/api/sessions")
	if strings.Contains(string(apiList), "ghost-orphan") {
		t.Fatalf("orphan leaked into /api/sessions: %s", apiList)
	}
	_ = time.Now
}
```

- [ ] **Step 2: Run + commit**

Run: `make e2e -run TestE2E_Reconcile_LiveButNotStored_KillsOrphan`
Expected: PASS.

```bash
git add test/e2e/e2e_reconcile_live_not_stored_test.go
git commit -m "test(e2e): Reconcile_LiveButNotStored_KillsOrphan"
```

### Task 4: BashExit_AutoClosesSession

**Files:**
- Create: `test/e2e/e2e_bash_exit_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestE2E_BashExit_AutoClosesSession(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sidKeep := createSession(t, tok, "keep")
	sidExit := createSession(t, tok, "exit-target")

	conn := dialWS(t, tok)
	drainStartupMessages(t, conn, time.Second)

	// Run `exit` in sidExit. bash exits voluntarily.
	_ = conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sidExit, "command": "exit",
	})
	// We don't necessarily get a done — the session is about to disappear.
	// Wait up to 5 seconds for the API to no longer list sidExit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body := getJSON(t, tok, "/api/sessions")
		if !strings.Contains(string(body), sidExit) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	body := getJSON(t, tok, "/api/sessions")
	if strings.Contains(string(body), sidExit) {
		t.Fatalf("sidExit %s never closed: %s", sidExit, body)
	}
	if !strings.Contains(string(body), sidKeep) {
		t.Fatalf("sidKeep %s was inadvertently closed: %s", sidKeep, body)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
git add test/e2e/e2e_bash_exit_test.go
git commit -m "test(e2e): BashExit_AutoClosesSession (and other sessions untouched)"
```

### Task 5: Stop_RestartsBashSameSession

**Files:**
- Create: `test/e2e/e2e_stop_respawn_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestE2E_Stop_RestartsBashSameSession(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "respawn")
	conn := dialWS(t, tok)
	drainStartupMessages(t, conn, time.Second)

	// Start sleep 60.
	_ = conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": "sleep 60"})
	cmdID := waitForStartedReturnID(t, conn, sid, 5*time.Second)

	// POST stop.
	req, _ := http.NewRequest("POST",
		baseHTTP+"/api/sessions/"+sid+"/commands/"+cmdID+"/stop", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("stop code = %d", resp.StatusCode)
	}

	// Wait for done event.
	waitForDone(t, conn, sid, cmdID, 5*time.Second)

	// Session is still present.
	body := getJSON(t, tok, "/api/sessions")
	if !strings.Contains(string(body), sid) {
		t.Fatalf("session disappeared after Stop: %s", body)
	}

	// We can run another command on the same session — proves respawn worked.
	_ = conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": "echo POST_RESPAWN"})
	cmd2 := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd2, 5*time.Second)
}
```

- [ ] **Step 2: Run + commit**

```bash
git add test/e2e/e2e_stop_respawn_test.go
git commit -m "test(e2e): Stop_RestartsBashSameSession (session survives Stop, next command works)"
```

### Task 6: Migration_OldSchemaImported

**Files:**
- Create: `test/e2e/e2e_migration_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestE2E_Migration_OldSchemaImported(t *testing.T) {
	// Seed the pod's /data with legacy layout, then restart alfred-server
	// (which removes sessions.json if present so migration re-runs).
	execInPod(t, `
		rm -f /data/sessions.json
		rm -rf /data/sessions
		mkdir -p /data/commands /data/outputs
		printf '%s' '{"id":"01HZA","command":"ls","status":"completed","started_at":"2026-06-10T10:00:00Z"}' > /data/commands/01HZA.json
		printf '%s' '{"id":"01HZB","command":"pwd","status":"completed","started_at":"2026-06-10T10:01:00Z"}' > /data/commands/01HZB.json
		printf 'tmp foo\n' > /data/outputs/01HZA.log
		printf '/tmp\n' > /data/outputs/01HZB.log
	`)
	restartAlfredProcess(t)

	tok, _ := login(t, testUser, testPassword)
	body := getJSON(t, tok, "/api/sessions")
	if !strings.Contains(string(body), "Imported") {
		t.Fatalf("Imported session missing: %s", body)
	}
	// Legacy dirs are gone.
	ls := execInPod(t, "ls /data 2>&1")
	if strings.Contains(ls, "commands") || strings.Contains(ls, "outputs") {
		t.Fatalf("legacy dirs still present: %s", ls)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
git add test/e2e/e2e_migration_test.go
git commit -m "test(e2e): Migration_OldSchemaImported"
```

### Task 7: RenamePersistsAcrossReload

**Files:**
- Create: `test/e2e/e2e_rename_persists_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestE2E_RenamePersistsAcrossReload(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "")
	body := []byte(`{"name":"training"}`)
	req, _ := http.NewRequest("PATCH", baseHTTP+"/api/sessions/"+sid, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("rename code = %d", resp.StatusCode)
	}
	restartAlfredProcess(t)
	tok2, _ := login(t, testUser, testPassword)
	listing := getJSON(t, tok2, "/api/sessions")
	if !strings.Contains(string(listing), "training") {
		t.Fatalf("rename did not survive restart: %s", listing)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
git add test/e2e/e2e_rename_persists_test.go
git commit -m "test(e2e): RenamePersistsAcrossReload"
```

---

## Task 8: Update README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update the top usage diagram**

Replace the existing ASCII art with one that reflects the sidebar + chat layout. Keep the text concise (the user shipped multi-session as a "personal-tool extension", not a marketing pitch).

Suggested replacement near the top:

```
┌──────────────────┬──────────────────────────────────────┐
│ + New chat       │ Active session name        ● Sign out │
├──────────────────┼──────────────────────────────────────┤
│ ACTIVE SESSIONS  │                                 [ ls ]│
│  • Session 1     │   CONTEXT.md Makefile deploy go.mod   │
│  • training      │   exit 0                              │
│  • db-debug ←sel │ ─────────────────────────────────────│
│                  │                                [ pwd ]│
│                  │   /Users/jesseliu/Desktop/...         │
│                  │   exit 0                              │
│                  ├──────────────────────────────────────┤
│                  │ ( Type a command…                ↑ )│
└──────────────────┴──────────────────────────────────────┘
```

Add a short paragraph below it:

> Up to 8 concurrent bash sessions, each independent (own cwd / env / aliases) but sharing the container's filesystem. `mkdir foo` in one session is visible from another. Sessions survive Go-process restarts (e.g. `kubectl rollout`); Pod restarts reset them but keep the per-command history on the PVC.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(readme): multi-session diagram + 1-paragraph intro"
```

---

## Task 9: Update CONTEXT.md

**Files:**
- Modify: `CONTEXT.md`

- [ ] **Step 1: Update the "Three invariants" section**

After invariant #1 ("Bash lifecycle ≠ WebSocket lifecycle"), append:

> **Strengthening (multi-session):** Bash lifecycle ≠ Go-process lifecycle either. The tmux server outlives alfred-server. `kubectl rollout` does NOT terminate in-flight commands. Pod restart DOES terminate them; this is an accepted trade-off documented in spec §1 non-goals.

In the "Non-obvious traps" table, add:

| Trap | What happens | Test |
|---|---|---|
| `tmux send-keys -l` sends `\n` as a literal character, not Enter | bash never executes the wrapper-script line; sentinel never fires; UI hangs | `TmuxRunner` splits into `SendText` + `SendEnter` (Plan 2); covered by `TestExecRunner_SendTextThenEnter_ExecutesCommand` |
| `tmux pipe-pane` holds the file open across a rename — naive truncate loses output | Output of subsequent commands ends up in the unlinked inode | `TmuxShell.TruncateConsumed` does stop-pipe → truncate → restart-pipe (Plan 2/3); covered by `TestStreamReader_TruncateAtIdleBoundary` |
| Empty FIFO would let bash block forever if Go-process is down | A FIFO consumer disappearing during Go restart SIGPIPEs bash | spec §3 chose regular file + offset over FIFO; covered by `TestStreamReader_ResumesFromPersistedOffset` |

In "Quick orientation":

| Change | Where to look first |
|---|---|
| Add a new tmux operation | `internal/shell/tmuxio/runner.go` |
| Modify session lifecycle | `internal/session/manager.go` |
| Change WS protocol | `internal/api/ws.go` + `web/src/lib/ws.ts` |
| Change the sidebar UI | `web/src/features/sessions/SessionsSidebar.tsx` |

- [ ] **Step 2: Commit**

```bash
git add CONTEXT.md
git commit -m "docs(context): multi-session invariants + new traps table + quick-orientation entries"
```

---

## Plan 18 acceptance

- All 5 recommended E2E PASS: `make e2e -run "Reconcile_LiveButNotStored|BashExit_AutoCloses|Stop_RestartsBash|Migration_OldSchema|RenamePersistsAcross"`.
- `Dockerfile` runtime stage installs tmux.
- CI workflow installs tmux on the Go-test job.
- `README.md` shows the multi-session layout and explains it in 1 paragraph.
- `CONTEXT.md` documents the new invariant strengthening + 3 new traps + 4 new quick-orientation entries.

## Plan 18 self-review checklist

- [ ] `grep "single bash" README.md CONTEXT.md` returns only historical references (commit log) or none.
- [ ] CI yaml still references `go-version: "1.25"` and `node-version: "20"` (don't bump as part of this plan).
- [ ] Each new E2E file has its own `//go:build e2e` build tag.

---

## End of multi-session plan series

After Plan 18 is merged, the multi-session via tmux feature is shipped.
- 18 plans, ~60 implementation tasks, ~80 commits.
- Test count: ~50 new unit tests, ~14 E2E scenarios (9 must-pass + 5 recommended).
- Spec coverage: every section of `docs/superpowers/specs/2026-06-11-multi-session-tmux-design.md` is implemented and exercised.
