# Multi-session Plan 14 — E2E `CrossSession_NoOutputBleed`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two sessions concurrently print 1000 lines each of distinct marker strings. Their persisted outputs must contain only their own marker; verifying that the sentinel parser's session-to-output routing is correct (no cross-contamination through the shared FanIn).

**Architecture:** Two sessions, two parallel commands launched via two WS connections so they really do run in parallel. Output verified via REST.

**Tech Stack:** Go E2E.

**Spec sections covered:** §6.2 (WS routing), implicit guarantee of §4.

---

## File Structure

```
test/e2e/
└── e2e_cross_session_no_bleed_test.go    # NEW
```

---

## Task 1: The scenario

**Files:**
- Create: `test/e2e/e2e_cross_session_no_bleed_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestE2E_CrossSession_NoOutputBleed(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sidA := createSession(t, tok, "A-secret")
	sidB := createSession(t, tok, "B-secret")

	connA := dialWS(t, tok)
	connB := dialWS(t, tok)
	drainStartupMessages(t, connA, time.Second)
	drainStartupMessages(t, connB, time.Second)

	// Launch both commands as close to simultaneously as we can.
	var wg sync.WaitGroup
	wg.Add(2)
	var idA, idB string
	go func() {
		defer wg.Done()
		_ = connA.WriteJSON(map[string]any{
			"type": "run", "sessionID": sidA,
			"command": "for i in $(seq 1 1000); do echo SECRET_A; done",
		})
		idA = waitForStartedReturnID(t, connA, sidA, 5*time.Second)
		waitForDone(t, connA, sidA, idA, 30*time.Second)
	}()
	go func() {
		defer wg.Done()
		_ = connB.WriteJSON(map[string]any{
			"type": "run", "sessionID": sidB,
			"command": "for i in $(seq 1 1000); do echo SECRET_B; done",
		})
		idB = waitForStartedReturnID(t, connB, sidB, 5*time.Second)
		waitForDone(t, connB, sidB, idB, 30*time.Second)
	}()
	wg.Wait()

	// Fetch persisted outputs.
	for _, c := range []struct {
		sid, id, wantMarker, forbidMarker string
	}{
		{sidA, idA, "SECRET_A", "SECRET_B"},
		{sidB, idB, "SECRET_B", "SECRET_A"},
	} {
		body := getJSON(t, tok, "/api/sessions/"+c.sid+"/commands/"+c.id)
		var full map[string]any
		_ = json.Unmarshal(body, &full)
		out, _ := full["output"].(string)
		gotWant := strings.Count(out, c.wantMarker)
		gotForbid := strings.Count(out, c.forbidMarker)
		if gotWant < 1000 {
			t.Fatalf("session %s: want %d %s, got %d", c.sid, 1000, c.wantMarker, gotWant)
		}
		if gotForbid != 0 {
			t.Fatalf("session %s: bleed! %s appeared %d times", c.sid, c.forbidMarker, gotForbid)
		}
	}
}
```

- [ ] **Step 2: Run**

Run: `make e2e -run TestE2E_CrossSession_NoOutputBleed`
Expected: PASS.

If `gotForbid != 0`, the sentinel routing in TmuxShell is mixing sessions — the wrapper command's cmdID is not matching the parser's `cur.id`, OR the FanIn is leaking events across sessions. Cross-check Plan 3 onParserEvent and Plan 6 FanIn.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_cross_session_no_bleed_test.go
git commit -m "test(e2e): CrossSession_NoOutputBleed (1000 SECRET_A vs SECRET_B parallel)"
```

---

## Plan 14 acceptance

- `make e2e -run TestE2E_CrossSession_NoOutputBleed` PASSes in <1 minute.

## Plan 14 self-review checklist

- [ ] Two separate WS connections used — proves the fan-in handles multiple clients too.
- [ ] Test waits for BOTH `done` events before reading persisted output.
- [ ] Forbidden marker count asserted as exactly 0, not "small".
