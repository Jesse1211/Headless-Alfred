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
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsMsgMulti is the multi-session WS message shape.
type wsMsgMulti struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionID,omitempty"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Name        string `json:"name,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

// createSession POSTs /api/sessions and returns the new session id.
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
	return out.ID
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
		var m wsMsgMulti
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
// container to come back online. Whether the same container respawns alfred or
// kubelet restarts the whole container is an implementation detail of the
// runtime; the test only relies on /healthz being reachable again afterwards.
func restartAlfredProcess(t *testing.T) {
	t.Helper()
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--",
		"sh", "-c", "pkill -KILL -f alfred-server || true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restart alfred: %v output=%s", err, out)
	}
	// Wait for the new process to bind :8080 (via port-forward to 18080).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseHTTP + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("alfred-server did not become ready within 30s after restart")
}

var _ = strings.TrimSpace
var _ = url.Parse
var _ = context.Background
var _ = fmt.Sprintf
