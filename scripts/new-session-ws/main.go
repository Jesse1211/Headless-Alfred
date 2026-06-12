// new-session-ws verifies the bug fix: connect a WS first, THEN create
// a new session via REST, then run a command in that new session — and
// confirm started/chunk/done arrive on the existing WS without a
// reconnect. Mirrors what the React frontend does when the user clicks
// "+ New chat" and immediately types a command.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

const base = "http://127.0.0.1:18080"

type wsMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID,omitempty"`
	CmdID     string `json:"cmdId,omitempty"`
	Data      string `json:"data,omitempty"`
	ExitCode  int    `json:"exitCode,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}

func run() error {
	tok, err := login()
	if err != nil {
		return err
	}

	// Step 1: connect WS BEFORE creating any session.
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(),
		"ws://127.0.0.1:18080/ws?token="+tok, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	fmt.Println("WS connected")

	// Step 2: REST-create a brand-new session immediately. waitForType
	// below filters by sessionID, so on-connect frames for other
	// sessions get silently skipped (no drain step needed, which avoids
	// poisoning the connection with an expired ReadDeadline).
	sid, err := createSession(tok, "ws-attach-test")
	if err != nil {
		return err
	}
	fmt.Printf("created session %s — now waiting for 'idle' frame on existing WS\n", sid)

	// Step 3: server should push an 'idle' frame for the new session.
	if err := waitForType(conn, sid, "idle", 3*time.Second); err != nil {
		return fmt.Errorf("never got idle for new session: %w", err)
	}
	fmt.Println("  ✓ got idle for new session without reconnect")

	// Step 4: send a 'run' for that session.
	if err := conn.WriteJSON(map[string]any{
		"type": "run", "sessionID": sid, "command": "echo HELLO_FROM_NEW_SESSION",
	}); err != nil {
		return err
	}

	// Step 5: expect started → chunk → done in the SAME WS connection.
	if err := waitForType(conn, sid, "started", 5*time.Second); err != nil {
		return fmt.Errorf("never got started: %w", err)
	}
	fmt.Println("  ✓ got started")
	gotChunk := false
	gotDone := false
	deadline := time.Now().Add(5 * time.Second)
	for !gotDone && time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if m.SessionID != sid {
			continue
		}
		if m.Type == "chunk" {
			gotChunk = true
		}
		if m.Type == "done" {
			gotDone = true
		}
	}
	if !gotChunk {
		return fmt.Errorf("never got chunk")
	}
	if !gotDone {
		return fmt.Errorf("never got done")
	}
	fmt.Println("  ✓ got chunk + done")
	return nil
}

func login() (string, error) {
	body, _ := json.Marshal(map[string]string{"user": "admin", "password": "e2etest"})
	resp, err := http.Post(base+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token, nil
}

func createSession(tok, name string) (string, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	req, _ := http.NewRequest("POST", base+"/api/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("create: %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID, nil
}

func drain(conn *websocket.Conn, perMsg time.Duration) {
	for {
		conn.SetReadDeadline(time.Now().Add(perMsg))
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
	}
	// Reset the deadline so subsequent reads aren't poisoned by the
	// timeout we just hit (gorilla websocket treats an expired read
	// deadline as a fatal connection error).
	conn.SetReadDeadline(time.Time{})
}

func waitForType(conn *websocket.Conn, sid, typ string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return err
		}
		if m.SessionID == sid && m.Type == typ {
			return nil
		}
	}
	return fmt.Errorf("timeout")
}
