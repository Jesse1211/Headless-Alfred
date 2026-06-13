//go:build e2e

package e2e

import (
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestE2E_EightConcurrentSleeps_NoSerialization(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	const N = 8
	sids := make([]string, N)
	conns := make([]*websocket.Conn, N)
	for i := 0; i < N; i++ {
		sids[i] = createSession(t, tok, "")
		conns[i] = dialWS(t, tok)
	}

	// Pre-load the started/done filters with the right sessionID. Each
	// waitForStarted / waitForDone already filters by sessionID, so
	// any idle/reattach frames for other sessions on the same WS get
	// silently skipped.
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
			id := waitForStarted(t, conns[i], sids[i], 5*time.Second)
			waitForDone(t, conns[i], sids[i], id, 15*time.Second)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if elapsed > 7*time.Second {
		t.Fatalf("8 concurrent sleep 5 took %v; expected <7s. Likely serialized.", elapsed)
	}
}
