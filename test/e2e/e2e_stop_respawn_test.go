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

	// Start sleep 60.
	_ = conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "sleep 60",
	})
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
	_ = conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "echo POST_RESPAWN",
	})
	cmd2 := waitForStartedReturnID(t, conn, sid, 5*time.Second)
	waitForDone(t, conn, sid, cmd2, 5*time.Second)
}
