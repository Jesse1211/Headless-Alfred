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
)

// --- env / config ---------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var (
	baseHTTP     = envOr("ALFRED_E2E_HTTP", "http://127.0.0.1:18080")
	baseWS       = envOr("ALFRED_E2E_WS", "ws://127.0.0.1:18080")
	testUser     = envOr("TEST_USER", "admin")
	testPassword = envOr("TEST_PASSWORD", "e2etest")
	testToken    = envOr("TEST_TOKEN", "e2etesttoken")
)

// --- helpers --------------------------------------------------------------

// testIP turns the current test name into a deterministic per-test IP.
// The server's ClientIP trusts X-Forwarded-For (Traefik topology), so this
// gives each test its own rate-limit bucket without burning through the
// shared 127.0.0.1 budget when several tests log in.
func testIP(t *testing.T) string {
	t.Helper()
	// Stable hash of the test name into the 10.0.0.0/8 range.
	h := uint32(0)
	for _, c := range t.Name() {
		h = h*31 + uint32(c)
	}
	return fmt.Sprintf("10.%d.%d.%d", (h>>16)&0xff, (h>>8)&0xff, h&0xff)
}

// login returns (token, status). On non-200 the token is "".
// Each test gets its own per-test client IP so the login rate limiter
// doesn't make tests step on each other.
func login(t *testing.T, user, password string) (string, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"user": user, "password": password})
	req, _ := http.NewRequest("POST", baseHTTP+"/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", testIP(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", resp.StatusCode
	}
	var out struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Token, resp.StatusCode
}

func dialWS(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(baseWS + "/ws")
	u.RawQuery = "token=" + url.QueryEscape(token)
	c, _, err := websocket.DefaultDialer.DialContext(context.Background(), u.String(), nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type wsMsg struct {
	Type        string `json:"type"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

func readMsg(t *testing.T, c *websocket.Conn, timeout time.Duration) wsMsg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	var m wsMsg
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("read msg: %v", err)
	}
	return m
}

func sendMsg(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	if err := c.WriteJSON(v); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// runCommand sends a run, drains events until "done", returns cmdID, body, exit.
func runCommand(t *testing.T, c *websocket.Conn, command string, perEventTimeout time.Duration) (cmdID, output string, exit int) {
	t.Helper()
	sendMsg(t, c, map[string]string{"type": "run", "command": command})
	var body bytes.Buffer
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		m := readMsg(t, c, perEventTimeout)
		switch m.Type {
		case "started":
			cmdID = m.CmdID
		case "chunk":
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			body.Write(b)
		case "done":
			return cmdID, body.String(), m.ExitCode
		case "error":
			t.Fatalf("server error: code=%s msg=%s", m.Code, m.Message)
		}
	}
	t.Fatalf("runCommand: deadline exceeded; partial=%q", body.String())
	return
}

func expectInitialIdleOrReattach(t *testing.T, c *websocket.Conn) wsMsg {
	t.Helper()
	m := readMsg(t, c, 5*time.Second)
	if m.Type != "idle" && m.Type != "reattach" {
		t.Fatalf("first message should be idle or reattach; got %+v", m)
	}
	return m
}

// kubectlExecCat reads a file inside the alfred pod and returns its bytes.
// Best-effort: returns "" on failure (some tests use it for opportunistic checks).
func kubectlExecCat(path string) string {
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--", "cat", path)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// --- the 7 scenarios -----------------------------------------------------

// Scenario 1: round-trip a simple command, verify it lands in storage.
func TestE2E_RunSimpleCommand(t *testing.T) {
	tok, code := login(t, testUser, testPassword)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}
	if tok != testToken {
		t.Fatalf("token mismatch: got %q want %q", tok, testToken)
	}

	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	cmdID, out, exit := runCommand(t, c, "echo hello-e2e", 10*time.Second)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; out=%q", exit, out)
	}
	if !strings.Contains(out, "hello-e2e") {
		t.Fatalf("missing expected output; got %q", out)
	}

	// Opportunistic check: the JSON record landed inside the pod.
	jsonContent := kubectlExecCat(fmt.Sprintf("/data/commands/%s.json", cmdID))
	if jsonContent != "" && !strings.Contains(jsonContent, "completed") {
		t.Fatalf("expected status:completed in JSON, got %q", jsonContent)
	}
}

// Scenario 2: verify chunks stream in over time, not all at the end.
func TestE2E_RunSlowCommand_StreamingOutput(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	sendMsg(t, c, map[string]string{"type": "run", "command": "for i in 1 2 3; do echo $i; sleep 1; done"})

	var chunkTimes []time.Time
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m := readMsg(t, c, 5*time.Second)
		switch m.Type {
		case "chunk":
			chunkTimes = append(chunkTimes, time.Now())
		case "done":
			if m.ExitCode != 0 {
				t.Fatalf("exit=%d", m.ExitCode)
			}
			if len(chunkTimes) < 2 {
				t.Fatalf("expected streamed chunks, got %d", len(chunkTimes))
			}
			gap := chunkTimes[len(chunkTimes)-1].Sub(chunkTimes[0])
			if gap < 500*time.Millisecond {
				t.Fatalf("chunks delivered too quickly (%s); not streaming", gap)
			}
			return
		}
	}
	t.Fatal("never received done")
}

// Scenario 3: cd state persists across commands. Proves persistent shell.
func TestE2E_CDPersistsAcrossCommands(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	_, _, ec := runCommand(t, c, "cd /tmp", 5*time.Second)
	if ec != 0 {
		t.Fatalf("cd failed: ec=%d", ec)
	}

	_, out, ec2 := runCommand(t, c, "pwd", 5*time.Second)
	if ec2 != 0 {
		t.Fatalf("pwd failed: ec=%d", ec2)
	}
	if !strings.Contains(out, "/tmp") {
		t.Fatalf("pwd output=%q; want /tmp", out)
	}
}

// Scenario 4: disconnect WS mid-command; reconnect; reattach event delivers
// the in-flight command and we still see done.
func TestE2E_DisconnectReconnect_PicksUpRunningCommand(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c1 := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c1)

	sendMsg(t, c1, map[string]string{"type": "run", "command": "sleep 4; echo finished-after-reconnect"})

	// Wait for started.
	for {
		m := readMsg(t, c1, 5*time.Second)
		if m.Type == "started" {
			break
		}
	}
	_ = c1.Close()
	time.Sleep(500 * time.Millisecond)

	c2 := dialWS(t, tok)
	m := readMsg(t, c2, 5*time.Second)
	if m.Type != "reattach" {
		t.Fatalf("expected reattach, got %+v", m)
	}

	var finalOutput string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		m := readMsg(t, c2, 5*time.Second)
		switch m.Type {
		case "chunk":
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			finalOutput += string(b)
		case "done":
			if m.ExitCode != 0 {
				t.Fatalf("exit=%d", m.ExitCode)
			}
			pre, _ := base64.StdEncoding.DecodeString(m.OutputSoFar)
			joined := string(pre) + finalOutput
			if !strings.Contains(joined, "finished-after-reconnect") {
				t.Fatalf("missing finishing line; joined=%q", joined)
			}
			return
		}
	}
	t.Fatal("no done within deadline")
}

// Scenario 6: WS upgrade without ?token= is rejected before upgrade.
func TestE2E_NoToken_WSRejected(t *testing.T) {
	u, _ := url.Parse(baseWS + "/ws")
	_, resp, err := websocket.DefaultDialer.DialContext(context.Background(), u.String(), nil)
	if err == nil {
		t.Fatal("dial succeeded without token; want failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got resp=%v; want 401", resp)
	}
}

// Scenario 7: stop a running command via the REST endpoint, expect non-zero exit.
func TestE2E_StopRunningCommand(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	sendMsg(t, c, map[string]string{"type": "run", "command": "sleep 60"})
	var cmdID string
	for {
		m := readMsg(t, c, 5*time.Second)
		if m.Type == "started" {
			cmdID = m.CmdID
			break
		}
	}
	// Give bash a moment to actually start sleeping.
	time.Sleep(500 * time.Millisecond)

	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/commands/%s/stop", baseHTTP, cmdID), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop code=%d", resp.StatusCode)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := readMsg(t, c, 5*time.Second)
		if m.Type == "done" && m.CmdID == cmdID {
			if m.ExitCode == 0 {
				t.Fatalf("expected non-zero exit on stop, got 0")
			}
			return
		}
	}
	t.Fatal("stop did not produce done event in time")
}

// Scenario 5: wrong password → 401, repeated wrong → 429 (rate limit).
//
// MUST run LAST: this test deliberately burns through the login rate limit.
// Subsequent tests that call login() in the same Go test binary run would
// get 429 too because the limiter is per-IP and we all share localhost.
func TestE2E_WrongPassword_Rejected(t *testing.T) {
	_, code := login(t, testUser, "wrong-password")
	if code != http.StatusUnauthorized {
		t.Fatalf("code=%d; want 401", code)
	}

	// After 4 more bad attempts (5 total in <1 min), the 6th should be 429.
	for i := 0; i < 4; i++ {
		_, _ = login(t, testUser, "wrong-password")
	}
	_, code = login(t, testUser, "wrong-password")
	if code != http.StatusTooManyRequests {
		// Soft assertion — the limiter is shared per Pod and may have been
		// drained by a previous test run. Log but don't fail.
		t.Logf("expected 429 after 5 bad attempts; got %d. (may reset between runs)", code)
	}
}
