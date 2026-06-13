//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/api"
)

func TestE2E_TwoSessions_FilesystemShared(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	if tok == "" {
		t.Fatal("login failed")
	}
	a := createSession(t, tok, "A")
	b := createSession(t, tok, "B")

	conn := dialWS(t, tok)

	dir := "/tmp/alfred-fs-test-" + a[:6]
	code, _ := runInSession(t, conn, a, "mkdir -p "+dir+" && touch "+dir+"/shared && echo done", 5*time.Second)
	if code != 0 {
		t.Fatalf("mkdir in A exit=%d", code)
	}
	code2, out := runInSession(t, conn, b, "ls "+dir, 5*time.Second)
	if code2 != 0 {
		t.Fatalf("ls in B exit=%d output=%q", code2, out)
	}
	if !strings.Contains(out, "shared") {
		t.Fatalf("B did not see A's file. ls output: %q", out)
	}
}

func TestE2E_GoRestart_SessionsSurvive(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "long")
	conn := dialWS(t, tok)

	// Kick off the long command and wait for started.
	cmd := "sleep 8 && echo HELLO_AFTER_RESTART"
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": cmd}); err != nil {
		t.Fatalf("write run: %v", err)
	}
	waitForStarted(t, conn, sid, 5*time.Second)
	_ = conn.Close()

	// Restart alfred-server while bash continues running in tmux.
	restartAlfredProcess(t)

	// Re-login (process is new), reconnect, drain reattach.
	tok2, _ := login(t, testUser, testPassword)
	conn2 := dialWS(t, tok2)
	// Wait for the EVENTUAL done event for our session.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn2.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := conn2.ReadJSON(&m); err != nil {
			t.Fatalf("read after restart: %v", err)
		}
		if m.SessionID == sid && m.Type == "done" {
			if m.ExitCode != 0 {
				t.Fatalf("exit=%d", m.ExitCode)
			}
			return
		}
	}
	t.Fatal("never saw done for the surviving command")
}

func TestE2E_GoRestart_DuringStreamingChunks(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "chunks")
	conn := dialWS(t, tok)

	cmd := `for i in $(seq 1 100); do echo $i; sleep 0.05; done`
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": cmd}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForStarted(t, conn, sid, 5*time.Second)

	// Read for ~2.5 seconds, then kill alfred.
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		_ = m
	}
	_ = conn.Close()
	restartAlfredProcess(t)

	// Re-attach, then poll the REST endpoint until the command shows up
	// with status completed and the output contains every integer 1..100.
	tok2, _ := login(t, testUser, testPassword)
	pollDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(pollDeadline) {
		req, _ := http.NewRequest("GET", baseHTTP+"/api/sessions/"+sid+"/commands", nil)
		req.Header.Set("Authorization", "Bearer "+tok2)
		req.Header.Set("X-Forwarded-For", testIP(t))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var list []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&list)
		resp.Body.Close()
		for _, c := range list {
			if status, _ := c["status"].(string); status == "completed" {
				// Fetch output.
				id := c["id"].(string)
				r2, _ := http.NewRequest("GET", baseHTTP+"/api/sessions/"+sid+"/commands/"+id, nil)
				r2.Header.Set("Authorization", "Bearer "+tok2)
				r2.Header.Set("X-Forwarded-For", testIP(t))
				resp2, _ := http.DefaultClient.Do(r2)
				var full map[string]any
				_ = json.NewDecoder(resp2.Body).Decode(&full)
				resp2.Body.Close()
				out, _ := full["output"].(string)
				// Verify every integer 1..100 is present.
				missing := 0
				for i := 1; i <= 100; i++ {
					if !strings.Contains(out, fmt.Sprintf("\n%d\n", i)) && !strings.HasPrefix(out, fmt.Sprintf("%d\n", i)) {
						missing++
					}
				}
				if missing > 0 {
					t.Fatalf("missing %d of 100 integers in output. output: %q", missing, out)
				}
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("command never completed after restart")
}
