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
	_ = waitForStartedReturnID(t, conn, sid, 5*time.Second)

	// Capture the bash PID inside the tmux session for later verification.
	pidsBefore := execInPod(t, "ps -eo pid,comm | awk '$2==\"sleep\" {print $1}' || true")
	if strings.TrimSpace(pidsBefore) == "" {
		t.Fatal("sleep 30 process never showed up in pod")
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

	// Wait up to 5s for the sleep 30 process to be reaped.
	gone := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pids := strings.TrimSpace(execInPod(t, "ps -eo pid,comm | awk '$2==\"sleep\" {print $1}' || true"))
		if pids == "" {
			gone = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !gone {
		t.Fatal("sleep 30 still running 5s after DELETE")
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
