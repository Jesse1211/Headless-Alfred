//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/api"
)

// TestE2E_GitCommit_RequiresDashM verifies the WS handler rejects a
// `git commit` invocation that lacks -m, before it can hang bash on a
// missing $EDITOR.
func TestE2E_GitCommit_RequiresDashM(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "git-commit-guard")
	conn := dialWS(t, tok)

	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "git commit --amend",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Expect an error frame for our session before any started/done.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("read: %v", err)
		}
		if m.SessionID != sid {
			continue
		}
		if m.Type == "error" && m.Code == "git_commit_needs_message" {
			if !strings.Contains(m.Message, "-m") {
				t.Fatalf("error message missing -m hint: %q", m.Message)
			}
			return
		}
		if m.Type == "started" {
			t.Fatalf("guard didn't fire — command was forwarded to bash")
		}
	}
	t.Fatal("never saw the guard error")
}
