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
