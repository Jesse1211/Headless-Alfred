// ws-smoke is a one-off integration tester: it spins up alfred-server as a
// subprocess, logs in via REST, opens a WebSocket, runs a command, asserts
// it sees started/chunk/done events and the persisted record. Equivalent to
// what the React frontend does end-to-end, but headless.
//
// Run with: go run ./scripts/ws-smoke
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ws-smoke FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("ws-smoke PASS")
}

func run() error {
	// Spin up the binary on a non-default port.
	dataDir, err := os.MkdirTemp("", "alfred-ws-smoke-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(dataDir)

	const port = "18091"
	const user = "admin"
	const pass = "test"
	const tok = "ws-smoke-token"

	cmd := exec.Command("./bin/alfred-server")
	cmd.Env = append(os.Environ(),
		"ALFRED_USER="+user,
		"ALFRED_PASSWORD="+pass,
		"ALFRED_TOKEN="+tok,
		"ALFRED_DATA_DIR="+dataDir,
		"ALFRED_ADDR=127.0.0.1:"+port,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := "http://127.0.0.1:" + port
	if err := waitReady(base, 5*time.Second); err != nil {
		return err
	}

	// Login.
	gotTok, err := login(base, user, pass)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if gotTok != tok {
		return fmt.Errorf("login token mismatch: got %q want %q", gotTok, tok)
	}

	// Dial WS.
	u, _ := url.Parse(strings.Replace(base, "http://", "ws://", 1) + "/ws")
	u.RawQuery = "token=" + tok
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), u.String(), nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// First message must be "idle".
	first, err := readMsg(conn, 3*time.Second)
	if err != nil {
		return fmt.Errorf("read idle: %w", err)
	}
	if first.Type != "idle" {
		return fmt.Errorf("first msg type = %q, want idle", first.Type)
	}

	// Run a command.
	if err := conn.WriteJSON(map[string]string{"type": "run", "command": "echo hello-ws-smoke"}); err != nil {
		return fmt.Errorf("send run: %w", err)
	}

	var (
		gotStarted, gotChunk, gotDone bool
		cmdID                         string
		body                          bytes.Buffer
	)
	deadline := time.Now().Add(10 * time.Second)
	for !gotDone && time.Now().Before(deadline) {
		m, err := readMsg(conn, 5*time.Second)
		if err != nil {
			return fmt.Errorf("read msg: %w", err)
		}
		switch m.Type {
		case "started":
			gotStarted = true
			cmdID = m.CmdID
		case "chunk":
			gotChunk = true
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			body.Write(b)
		case "done":
			gotDone = true
			if m.CmdID != cmdID {
				return fmt.Errorf("done cmdID = %q want %q", m.CmdID, cmdID)
			}
			if m.ExitCode != 0 {
				return fmt.Errorf("exit code = %d, want 0", m.ExitCode)
			}
		case "error":
			return fmt.Errorf("server error: %s: %s", m.Code, m.Message)
		}
	}
	if !(gotStarted && gotChunk && gotDone) {
		return fmt.Errorf("missed events: started=%v chunk=%v done=%v", gotStarted, gotChunk, gotDone)
	}
	if !strings.Contains(body.String(), "hello-ws-smoke") {
		return fmt.Errorf("body = %q, want contains hello-ws-smoke", body.String())
	}

	// Verify the record landed in the REST API too.
	rec, err := getCommand(base, gotTok, cmdID)
	if err != nil {
		return fmt.Errorf("get command: %w", err)
	}
	if rec.Status != "completed" {
		return fmt.Errorf("rec.status = %q, want completed", rec.Status)
	}
	if !strings.Contains(rec.Output, "hello-ws-smoke") {
		return fmt.Errorf("rec.output = %q, want contains hello-ws-smoke", rec.Output)
	}

	return nil
}

func waitReady(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server never became ready at %s", base)
}

func login(base, user, pass string) (string, error) {
	body, _ := json.Marshal(map[string]string{"user": user, "password": pass})
	resp, err := http.Post(base+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

type record struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output string `json:"output"`
}

func getCommand(base, tok, id string) (record, error) {
	req, _ := http.NewRequest("GET", base+"/api/commands/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return record{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return record{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var r record
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return record{}, err
	}
	return r, nil
}

type wsMsg struct {
	Type     string `json:"type"`
	CmdID    string `json:"cmdId,omitempty"`
	Data     string `json:"data,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
}

func readMsg(c *websocket.Conn, timeout time.Duration) (wsMsg, error) {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	var m wsMsg
	if err := c.ReadJSON(&m); err != nil {
		return wsMsg{}, err
	}
	return m, nil
}
