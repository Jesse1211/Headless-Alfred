# Multi-session Plan 17 — E2E `SessionLimit` + delete 4 superseded single-bash tests

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two things:
1. New scenario `TestE2E_SessionLimit`: create 8 sessions, the 9th POST returns 422 with `session_limit`.
2. Delete the four E2E tests superseded by Plans 11-16:
   - `TestE2E_RunSlowCommand_StreamingOutput` (covered by Plan 11 chunks test)
   - `TestE2E_CDPersistsAcrossCommands` (covered by Plan 11 fs-share test indirectly)
   - `TestE2E_NoToken_WSRejected` (unit-tested in api/ws_test)
   - `TestE2E_WrongPassword_Rejected` (unit-tested in api/login_test)

**Architecture:** New test in `e2e_session_limit_test.go`. The deletions happen in place in `e2e_test.go`.

**Tech Stack:** Go E2E.

**Spec sections covered:** §6.1 session_limit code; §9 (superseded tests cleanup).

---

## File Structure

```
test/e2e/
├── e2e_test.go                    # MODIFY: delete 4 functions
└── e2e_session_limit_test.go      # NEW
```

---

## Task 1: SessionLimit scenario

**Files:**
- Create: `test/e2e/e2e_session_limit_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestE2E_SessionLimit(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	for i := 0; i < 8; i++ {
		_ = createSession(t, tok, "")
	}
	// 9th must 422.
	req, _ := http.NewRequest("POST", baseHTTP+"/api/sessions", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	body, _ := ioReadAll(resp.Body)
	var msg map[string]any
	_ = json.Unmarshal(body, &msg)
	if code, _ := msg["code"].(string); code != "session_limit" {
		t.Fatalf("expected code session_limit, got body=%s", body)
	}
	if !strings.Contains(string(body), "session_limit") {
		t.Fatalf("body missing session_limit: %s", body)
	}
}

// ioReadAll avoids importing io/ioutil; std-only Go 1.16+ uses io.ReadAll.
func ioReadAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}
```

- [ ] **Step 2: Run**

Run: `make e2e -run TestE2E_SessionLimit`
Expected: PASS.

Note: If this test runs after others that already created sessions and didn't clean up, the limit gate may already be reached. Run with a fresh cluster or `make e2e-setup && make e2e -run TestE2E_SessionLimit` for determinism. If you want robustness across reuse, add a teardown that DELETEs every existing session before the create loop — left as a follow-up.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_session_limit_test.go
git commit -m "test(e2e): SessionLimit (9th POST returns 422 session_limit)"
```

---

## Task 2: Delete 4 superseded tests

**Files:**
- Modify: `test/e2e/e2e_test.go`

- [ ] **Step 1: Open `test/e2e/e2e_test.go` and delete the four functions**

Find and remove the entire bodies of:
- `TestE2E_RunSlowCommand_StreamingOutput`
- `TestE2E_CDPersistsAcrossCommands`
- `TestE2E_NoToken_WSRejected`
- `TestE2E_WrongPassword_Rejected`

Keep:
- `TestE2E_RunSimpleCommand` (smoke)
- `TestE2E_DisconnectReconnect_PicksUpRunningCommand` (reattach)
- `TestE2E_StopRunningCommand` (Stop path — though Plan 18's recommended scenario `Stop_RestartsBashSameSession` is tighter)

For each kept test, do all three of these:

1. Create a session at the top: `sid := createSession(t, tok, "...")`
2. Send the run message with `sessionID`: `conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": "pwd"})`
3. **Switch the message decoder** from the legacy `wsMsg` struct (no SessionID field) to `wsMsgMulti` (Plan 11). The old `wsMsg` would silently drop the new `sessionID` field, so assertions like "did the right session report idle?" turn into false positives.

```go
// before:
var m wsMsg
_ = conn.ReadJSON(&m)
// after:
var m wsMsgMulti
_ = conn.ReadJSON(&m)
// and, for assertions that branch on session:
if m.SessionID != sid { continue }
```

If the test does not branch on sessionID (e.g., `TestE2E_StopRunningCommand` just waits for `done`), the switch is type-only — but **do switch** to keep one struct across the file.

- [ ] **Step 2: Run remaining tests**

Run: `make e2e -run "TestE2E_RunSimpleCommand|TestE2E_DisconnectReconnect|TestE2E_StopRunningCommand"`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test(e2e): delete 4 superseded single-bash tests; thread sessionID into 3 carried over"
```

---

## Plan 17 acceptance

- `make e2e -run TestE2E_SessionLimit` PASSes.
- The 3 carried-over tests still PASS.
- The 4 deleted tests no longer exist.

## Plan 17 self-review checklist

- [ ] `e2e_test.go` no longer has any reference to the deleted test names.
- [ ] Each carried-over test passes a `sessionID` in every WS message.
- [ ] Session limit test has a note in the comment about needing a fresh cluster (or running first).
