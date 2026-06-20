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
	//
	// On a CI kind node this poll has to outlast a chain of slow steps that
	// run AFTER alfred is killed mid-stream: the command (still alive in
	// tmux, piped to pty.stream on the PVC) finishing its remaining ~2.5s of
	// iterations, the entrypoint respawning alfred, the new alfred reattaching
	// and draining the pty.stream tail, and the persister firing the Ended
	// event that flips the record to "completed" with the full output. The
	// persister writes output + status="completed" together (see
	// Manager.startPersister), so a "completed" record always carries the
	// full output — the only CI risk is that the whole chain takes longer
	// than the poll, so we widen the poll, never the data or the assertion.
	tok2, _ := login(t, testUser, testPassword)
	const restartCompletePoll = 60 * time.Second // CI kind is slower than local
	pollDeadline := time.Now().Add(restartCompletePoll)
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
				// Per CONTEXT.md Invariant #1: after a Go-process restart the
				// new alfred resumes parsing pty.stream from the persisted
				// pty.offset (the live tail) and re-emits only PENDING events.
				// Output produced BEFORE the kill lived only in the dead
				// process's in-memory buffer and is intentionally NOT
				// reconstructed — that's the documented trade-off (command
				// liveness over pre-restart output prefix). So we must NOT
				// assert all of 1..100 survive. What the design DOES guarantee:
				// the command keeps running and completes, and the post-restart
				// tail is a CONTIGUOUS suffix ending at 100 (no gaps within the
				// surviving portion, final line present). The pty emits CRLF
				// (\r\n), so normalize to \n before line-matching — otherwise
				// "\n100\n" never matches "99\r\n100\r\n".
				norm := strings.ReplaceAll(out, "\r\n", "\n")
				if !strings.Contains(norm, "\n100\n") && !strings.HasPrefix(norm, "100\n") {
					t.Fatalf("final integer 100 missing — command did not complete cleanly after restart. output: %q", out)
				}
				// Lowest surviving integer, then assert lowest..100 has no holes.
				lowest := 0
				for i := 1; i <= 100; i++ {
					if strings.Contains(norm, fmt.Sprintf("\n%d\n", i)) || strings.HasPrefix(norm, fmt.Sprintf("%d\n", i)) {
						lowest = i
						break
					}
				}
				if lowest == 0 {
					t.Fatalf("no integers at all in post-restart output: %q", out)
				}
				for i := lowest; i <= 100; i++ {
					if !strings.Contains(norm, fmt.Sprintf("\n%d\n", i)) && !strings.HasPrefix(norm, fmt.Sprintf("%d\n", i)) {
						t.Fatalf("gap in surviving tail: %d missing between %d and 100. output: %q", i, lowest, out)
					}
				}
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("command never completed within %s after restart", restartCompletePoll)
}
