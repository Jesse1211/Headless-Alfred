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
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jesseliu/headless-alfred/internal/api"
)

// TestMain wipes any leftover sessions from a previous run before the
// suite starts. Without this, leftover sessions from a manually-aborted
// run (or the previous CI invocation in a reused cluster) consume slots
// and push TestE2E_SessionLimit or any later createSession over the
// §6.1 cap.
func TestMain(m *testing.M) {
	wipeAllSessions()
	os.Exit(m.Run())
}

func wipeAllSessions() {
	body, _ := json.Marshal(map[string]string{"user": "admin", "password": "e2etest"})
	resp, err := http.Post(baseHTTP+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var out struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		return
	}
	req, _ := http.NewRequest("GET", baseHTTP+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+out.Token)
	listResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer listResp.Body.Close()
	var sessions []struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&sessions)
	for _, s := range sessions {
		dreq, _ := http.NewRequest("DELETE", baseHTTP+"/api/sessions/"+s.ID, nil)
		dreq.Header.Set("Authorization", "Bearer "+out.Token)
		dr, err := http.DefaultClient.Do(dreq)
		if err == nil {
			dr.Body.Close()
		}
	}
}

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
	h := uint32(0)
	for _, c := range t.Name() {
		h = h*31 + uint32(c)
	}
	return fmt.Sprintf("10.%d.%d.%d", (h>>16)&0xff, (h>>8)&0xff, h&0xff)
}

// login returns (token, status). On non-200 the token is "".
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

// --- scenarios -----------------------------------------------------------

// Scenario: simple round-trip command, verify exit + persisted output.
// The single-session "/api/commands/{id}" path was retired in Plan 5 — we
// use multi-session createSession + the session-scoped command record.
func TestE2E_RunSimpleCommand(t *testing.T) {
	tok, code := login(t, testUser, testPassword)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}
	if tok != testToken {
		t.Fatalf("token mismatch: got %q want %q", tok, testToken)
	}
	sid := createSession(t, tok, "simple")
	c := dialWS(t, tok)

	exit, out := runInSession(t, c, sid, "echo hello-e2e", 10*time.Second)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; out=%q", exit, out)
	}
	if !strings.Contains(out, "hello-e2e") {
		t.Fatalf("missing expected output; got %q", out)
	}
}

// Scenario: disconnect WS mid-command, reconnect, the reattach event
// delivers the in-flight command and we still see done.
func TestE2E_DisconnectReconnect_PicksUpRunningCommand(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "reconnect")
	c1 := dialWS(t, tok)

	if err := c1.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid,
		"command": "sleep 4; echo finished-after-reconnect",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// Wait for started for our session.
	waitForStarted(t, c1, sid, 5*time.Second)
	_ = c1.Close()
	time.Sleep(500 * time.Millisecond)

	c2 := dialWS(t, tok)
	// Drain on-connect frames until we see a reattach for our session.
	var pre string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = c2.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := c2.ReadJSON(&m); err != nil {
			t.Fatalf("read: %v", err)
		}
		if m.SessionID == sid && m.Type == "reattach" {
			b, _ := base64.StdEncoding.DecodeString(m.OutputSoFar)
			pre = string(b)
			break
		}
	}

	var post bytes.Buffer
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_ = c2.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := c2.ReadJSON(&m); err != nil {
			t.Fatalf("read: %v", err)
		}
		if m.SessionID != sid {
			continue
		}
		switch m.Type {
		case "chunk":
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			post.Write(b)
		case "done":
			if m.ExitCode != 0 {
				t.Fatalf("exit=%d", m.ExitCode)
			}
			joined := pre + post.String()
			if !strings.Contains(joined, "finished-after-reconnect") {
				t.Fatalf("missing finishing line; joined=%q", joined)
			}
			return
		}
	}
	t.Fatal("no done within deadline")
}

// Scenario: stop a running command via the (session-scoped) Stop REST
// endpoint and expect a non-zero exit on the WS done frame.
func TestE2E_StopRunningCommand(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "stop-me")
	c := dialWS(t, tok)

	if err := c.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "sleep 60",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	cmdID := waitForStarted(t, c, sid, 5*time.Second)
	// Give bash a moment to actually start sleeping.
	time.Sleep(500 * time.Millisecond)

	req, _ := http.NewRequest("POST",
		fmt.Sprintf("%s/api/sessions/%s/commands/%s/stop", baseHTTP, sid, cmdID),
		nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
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
		_ = c.SetReadDeadline(deadline)
		var m api.OutMsg
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("read: %v", err)
		}
		if m.SessionID == sid && m.Type == "done" && m.CmdID == cmdID {
			if m.ExitCode == 0 {
				t.Fatalf("expected non-zero exit on stop, got 0")
			}
			return
		}
	}
	t.Fatal("stop did not produce done event in time")
}
