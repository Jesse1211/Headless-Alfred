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

	// Establish some completed command history so the rebuild can be
	// observed as "session reappears with history intact". runInSession
	// already filters by sessionID, so the on-connect idle/reattach frames
	// for other sessions are skipped automatically.
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

	// Kill tmux, then force alfred-server to restart so Reconcile() runs.
	// Manager.Reconcile detects sessions present in store but missing live
	// (stored \ live), rebuilds tmux sessions for them, and sweeps any
	// status=running command records to status=interrupted.
	killTmuxServerInPod(t)
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
