//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestE2E_CloseSession_RunningCommandTerminated(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "delete-me")
	conn := dialWS(t, tok)

	_ = conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "sleep 30",
	})
	_ = waitForStarted(t, conn, sid, 5*time.Second)

	// Capture THIS session's sleep PID via its tmux pane, not a pod-global
	// `comm==sleep` match. Sibling E2E tests spawn their own long sleeps in
	// the same pod (and Stop/kill paths can orphan a sleep under tini that
	// lives out its full duration), so a global match would observe an
	// unrelated process and false-fail. Scope to our pane's child.
	sleepPID := strings.TrimSpace(execInPod(t,
		"pgrep -P $(tmux -S /data/alfred-tmux.sock list-panes -t "+sid+
			" -F '#{pane_pid}') -x sleep | head -1 || true"))
	if sleepPID == "" {
		t.Fatal("sleep 30 process never showed up under this session's pane")
	}

	// DELETE the session.
	req, _ := http.NewRequest("DELETE", baseHTTP+"/api/sessions/"+sid, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("delete: code=%d", resp.StatusCode)
	}

	// Wait for the sleep 30 process to be reaped. DELETE triggers
	// tmux kill-session → SIGHUP/SIGTERM of the bash subtree → kernel
	// reaping. That whole chain is near-instant locally, but in a
	// resource-constrained CI kind node (shared CPU, slow process
	// scheduling) it can take several seconds. We only widen HOW LONG we
	// wait for an outcome that does happen — we still assert the process
	// is gone.
	const reapWait = 15 * time.Second // CI kind is slower than local
	gone := false
	deadline := time.Now().Add(reapWait)
	for time.Now().Before(deadline) {
		// Poll ONLY our captured PID — immune to other tests' sleeps.
		alive := strings.TrimSpace(execInPod(t, "ps -p "+sleepPID+" -o pid= 2>/dev/null || true"))
		if alive == "" {
			gone = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !gone {
		t.Fatalf("our session's sleep (pid %s) still running %s after DELETE", sleepPID, reapWait)
	}

	// Session directory under /data/sessions/<sid> must be gone.
	dirCheck := strings.TrimSpace(execInPod(t,
		"if [ -d /data/sessions/"+sid+" ]; then echo PRESENT; else echo GONE; fi"))
	if dirCheck != "GONE" {
		t.Fatalf("/data/sessions/%s still present (got %q)", sid, dirCheck)
	}

	// The DELETED session no longer appears in /api/sessions.
	body := getJSON(t, tok, "/api/sessions")
	if strings.Contains(string(body), sid) {
		t.Fatalf("deleted session still listed: %s", body)
	}
}
