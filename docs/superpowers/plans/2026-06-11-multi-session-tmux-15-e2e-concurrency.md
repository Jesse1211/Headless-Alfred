# Multi-session Plan 15 — E2E `EightConcurrentSleeps_NoSerialization`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 8 sessions each run `sleep 5` simultaneously. All 8 must complete within ~6 seconds wall-clock. A serialized implementation (e.g., a global lock in TmuxShell or Manager) would take ~40 seconds — making this the cheapest regression test for accidental serialization.

**Architecture:** 8 sessions, 8 goroutines launching one `sleep 5` each, wall-clock measurement.

**Tech Stack:** Go E2E.

**Spec sections covered:** §12 acceptance (concurrent sleeps).

---

## File Structure

```
test/e2e/
└── e2e_concurrent_sleeps_test.go    # NEW
```

---

## Task 1: The scenario

**Files:**
- Create: `test/e2e/e2e_concurrent_sleeps_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"sync"
	"testing"
	"time"
)

func TestE2E_EightConcurrentSleeps_NoSerialization(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	const N = 8
	sids := make([]string, N)
	conns := make([]*websocket.Conn, N)
	for i := 0; i < N; i++ {
		sids[i] = createSession(t, tok, "")
		conns[i] = dialWS(t, tok)
		drainStartupMessages(t, conns[i], 500*time.Millisecond)
	}

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			_ = conns[i].WriteJSON(map[string]any{
				"type": "run", "sessionID": sids[i], "command": "sleep 5",
			})
			id := waitForStartedReturnID(t, conns[i], sids[i], 5*time.Second)
			waitForDone(t, conns[i], sids[i], id, 15*time.Second)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if elapsed > 7*time.Second {
		t.Fatalf("8 concurrent sleep 5 took %v; expected <7s. Likely serialized.", elapsed)
	}
}
```

(Add `"github.com/gorilla/websocket"` to imports.)

- [ ] **Step 2: Run**

Run: `make e2e -run TestE2E_EightConcurrentSleeps_NoSerialization`
Expected: PASS in ~5-6 seconds.

If it takes 10+ seconds, there is serialization somewhere — usually a global `sync.Mutex` shared across TmuxShells. Check Plan 3 (TmuxShell's `mu`), Plan 4 (Manager's `mu`), and Plan 6 (writeMu in ws.go — note that `writeMu` is per-client, intentional).

- [ ] **Step 3: Commit**

```bash
git add test/e2e/e2e_concurrent_sleeps_test.go
git commit -m "test(e2e): EightConcurrentSleeps_NoSerialization (cheap concurrency regression)"
```

---

## Plan 15 acceptance

- `make e2e -run TestE2E_EightConcurrentSleeps_NoSerialization` PASSes in <7 seconds.

## Plan 15 self-review checklist

- [ ] 8 separate WS connections — proves it's not the WS reader that's serializing.
- [ ] Wall-clock budget gives 2 seconds slack (sleep 5 + 2s).
