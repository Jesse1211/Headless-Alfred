//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jesseliu/headless-alfred/internal/api"
)

// createSession POSTs /api/sessions and returns the new session id.
// Each created session registers a t.Cleanup that DELETEs it after the
// test finishes, so back-to-back tests don't accumulate sessions and
// trip the §6.1 session_limit on later tests.
func createSession(t *testing.T, token, name string) string {
	t.Helper()
	body := []byte("{}")
	if name != "" {
		body, _ = json.Marshal(map[string]string{"name": name})
	}
	req, _ := http.NewRequest("POST", baseHTTP+"/api/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", testIP(t))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		var msg map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		t.Fatalf("create session: code=%d body=%v", resp.StatusCode, msg)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	t.Cleanup(func() { deleteSession(token, out.ID) })
	return out.ID
}

// deleteSession best-effort cleanup. Errors are silenced; if a test left
// the session in a state where DELETE fails, the next test's TestMain or
// TestE2E_SessionLimit's prelude will hit the limit and surface the leak.
func deleteSession(token, sid string) {
	req, _ := http.NewRequest("DELETE", baseHTTP+"/api/sessions/"+sid, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// runInSession sends one command and waits for done; returns full output.
func runInSession(t *testing.T, conn *websocket.Conn, sessionID, command string, timeout time.Duration) (int, string) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sessionID, "command": command,
	}); err != nil {
		t.Fatalf("ws run: %v", err)
	}
	deadline := time.Now().Add(timeout)
	var buf bytes.Buffer
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("ws read: %v", err)
		}
		if m.SessionID != "" && m.SessionID != sessionID {
			continue // not for us
		}
		if m.Type == "chunk" {
			data, _ := base64.StdEncoding.DecodeString(m.Data)
			buf.Write(data)
		}
		if m.Type == "done" {
			return m.ExitCode, buf.String()
		}
	}
	t.Fatalf("timeout waiting for done; collected so far: %q", buf.String())
	return -1, ""
}

// restartAlfredProcess SIGKILLs alfred-server inside the pod and waits for the
// entrypoint respawn loop to bring it back. The tmux server (daemonized,
// reparented to PID 1 = tini) survives the alfred restart, so in-flight
// commands keep running and the new alfred can reattach via Manager.Reconcile.
//
// We match alfred-server by exact comm name via pgrep -x (NOT pkill -f),
// otherwise the wrapping `sh -c "..."` process would itself match (its cmdline
// contains the literal "alfred-server") and get SIGKILLed mid-exec, returning
// exit code 137 from kubectl.
//
// The local kubectl port-forward dies the moment alfred's :8080 listener is
// reset (connection reset by peer → pf process exits). After the kill we
// always re-launch port-forward and poll healthz over the new tunnel.
func restartAlfredProcess(t *testing.T) {
	t.Helper()
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--",
		"sh", "-c", `p=$(pgrep -x alfred-server); [ -n "$p" ] && kill -KILL $p; true`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restart alfred: %v output=%s", err, out)
	}

	// The killed alfred resets its tcp listener; the local kubectl
	// port-forward sees "connection reset by peer" and exits. We have to
	// re-launch it AFTER the new alfred is actually listening on :8080
	// inside the pod — starting pf earlier causes its first probe-attempt
	// to land while pod:8080 still refuses connections, killing the pf
	// process again. Strategy: poll pod:8080 from inside the pod via
	// kubectl exec, then start pf, then poll healthz.
	waitForAlfredListeningInPod(t, 30*time.Second)
	relaunchPortForward(t)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseHTTP + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		// pf may have died on first probe; restart it and try again.
		relaunchPortForward(t)
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("alfred-server did not become ready within 30s after restart")
}

// waitForAlfredListeningInPod polls inside the pod (via kubectl exec) for
// alfred to be listening on :8080. Go's net.Listen("tcp", ":8080") opens
// an IPv6 dual-stack socket, which shows up only in /proc/net/tcp6 — not
// /proc/net/tcp. We scan both for a LISTEN row (state 0A) at port 0x1F90.
func waitForAlfredListeningInPod(t *testing.T, timeout time.Duration) {
	t.Helper()
	check := `awk '$2 ~ /:1F90$/ && $4 == "0A" {found=1} END {exit !found}' /proc/net/tcp /proc/net/tcp6`
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if exec.Command("kubectl", "-n", "alfred", "exec",
			"deployment/alfred", "--", "sh", "-c", check,
		).Run() == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("alfred never started listening on :8080 inside pod")
}

// relaunchPortForward kills any stale `kubectl port-forward svc/alfred`
// process and starts a fresh one in the background. Idempotent. Mirrors
// what test/e2e/setup.sh does — we don't shell out to setup.sh because we
// don't want its image-rebuild side effects on every test restart.
func relaunchPortForward(t *testing.T) {
	t.Helper()
	_ = exec.Command("pkill", "-f", "kubectl.*port-forward.*svc/alfred").Run()
	// pkill is async; give the OS a moment to release :18080.
	time.Sleep(300 * time.Millisecond)
	logFile, err := os.Create("/tmp/alfred-e2e-pf.log")
	if err != nil {
		t.Fatalf("open pf log: %v", err)
	}
	defer logFile.Close()
	pf := exec.Command("kubectl", "-n", "alfred", "port-forward",
		"svc/alfred", "18080:8080")
	pf.Stdout = logFile
	pf.Stderr = logFile
	if err := pf.Start(); err != nil {
		t.Fatalf("start port-forward: %v", err)
	}
	// Detach so the OS reaps it; we don't track the PID further. The next
	// restart will pkill any stale instance.
	_ = pf.Process.Release()
}

// killTmuxServerInPod terminates the tmux server inside the alfred pod,
// leaving the alfred-server process alive and the per-session command
// JSONs untouched on the PVC. Used to simulate "container survived,
// tmux died" — which is functionally the same as the §4.7 "stored \
// live" reconciliation branch from alfred-server's perspective.
func killTmuxServerInPod(t *testing.T) {
	t.Helper()
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--",
		"sh", "-c", "pkill -KILL tmux || true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kill tmux in pod: %v output=%s", err, out)
	}
}

// execInPod runs `sh -c "<script>"` inside the alfred pod and returns
// stdout. Used to inspect bash process state and filesystem during E2E.
func execInPod(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--",
		"sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("execInPod %q: %v output=%s", script, err, out)
	}
	return string(out)
}

// waitForStarted blocks until a 'started' frame for sessionID arrives,
// returning its cmdId. Existing callers that don't need the id can
// ignore the return.
func waitForStarted(t *testing.T, conn *websocket.Conn, sessionID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatalf("ws read: %v", err)
		}
		if m.SessionID == sessionID && m.Type == "started" {
			return m.CmdID
		}
	}
	t.Fatal("no started")
	return ""
}

// e2eSession bootstraps a fully-set-up test session: log in, create a
// fresh session, open a WS. The cleanup that dialWS already registers
// closes the conn; createSession's t.Cleanup deletes the session. So a
// test that uses e2eSession only needs to drive the conn — no manual
// teardown.
func e2eSession(t *testing.T, name string) (token, sessionID string, conn *websocket.Conn) {
	t.Helper()
	token, _ = login(t, testUser, testPassword)
	if token == "" {
		t.Fatal("login failed")
	}
	sessionID = createSession(t, token, name)
	conn = dialWS(t, token)
	return token, sessionID, conn
}

var _ = strings.TrimSpace
var _ = url.Parse
var _ = context.Background
var _ = fmt.Sprintf
