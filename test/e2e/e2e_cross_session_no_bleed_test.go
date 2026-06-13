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

	// Launch both commands as close to simultaneously as we can.
	// waitForStarted / waitForDone already filter by sessionID, so
	// the on-connect idle/reattach frames for unrelated sessions get
	// skipped automatically.
	var wg sync.WaitGroup
	wg.Add(2)
	var idA, idB string
	go func() {
		defer wg.Done()
		_ = connA.WriteJSON(map[string]any{
			"type": "run", "sessionID": sidA,
			"command": "for i in $(seq 1 1000); do echo SECRET_A; done",
		})
		idA = waitForStarted(t, connA, sidA, 5*time.Second)
		waitForDone(t, connA, sidA, idA, 30*time.Second)
	}()
	go func() {
		defer wg.Done()
		_ = connB.WriteJSON(map[string]any{
			"type": "run", "sessionID": sidB,
			"command": "for i in $(seq 1 1000); do echo SECRET_B; done",
		})
		idB = waitForStarted(t, connB, sidB, 5*time.Second)
		waitForDone(t, connB, sidB, idB, 30*time.Second)
	}()
	wg.Wait()

	// Wait for the persister to flush both records before reading them.
	waitForPersisted(t, tok, sidA, idA, 5*time.Second)
	waitForPersisted(t, tok, sidB, idB, 5*time.Second)

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
