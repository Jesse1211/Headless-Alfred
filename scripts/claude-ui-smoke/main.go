// claude-ui-smoke verifies the full Claude UI backend pipeline:
//
//  1. login + create alfred session
//  2. dial WS
//  3. enter_claude { renderer: "ui" } → claude_entered { renderer: "ui" }
//  4. claude_prompt { text: "ping" } → expect a stream of claude_event
//     frames, starting with system.init and ending with a result frame.
//
// The test does NOT require a valid ANTHROPIC OAuth credential. If
// the pod has no credentials.json, claude will print an
// authentication error inside the stream-json — we still get
// well-formed claude_event frames; the result event will have
// is_error=true. We verify the pipe is intact regardless of whether
// the model actually responded.
//
// Run with: go run ./scripts/claude-ui-smoke
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const base = "http://127.0.0.1:18080"

type wsMsg struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionID,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Renderer  string          `json:"renderer,omitempty"`
	EventKind string          `json:"eventKind,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}

func run() error {
	// --- 1. log in ---
	tok, err := login()
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if tok == "" {
		return fmt.Errorf("login returned empty token")
	}
	fmt.Println("✓ login")

	// --- 2. create alfred session ---
	wipeSessions(tok)
	sid, err := createSession(tok, "claude-ui-smoke")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	fmt.Printf("✓ session created: %s\n", sid)

	// --- 3. dial WS ---
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(),
		"ws://127.0.0.1:18080/ws?token="+tok, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()
	fmt.Println("✓ WS connected")

	// --- 4. enter_claude { renderer: ui } ---
	if err := conn.WriteJSON(map[string]any{
		"type": "enter_claude", "sessionID": sid, "renderer": "ui",
	}); err != nil {
		return err
	}
	// Expect claude_entered for our session.
	if err := waitFor(conn, sid, func(m wsMsg) bool {
		return m.Type == "claude_entered" && m.Renderer == "ui"
	}, 5*time.Second); err != nil {
		return fmt.Errorf("waiting for claude_entered{ui}: %w", err)
	}
	fmt.Println("✓ entered claude UI mode")

	// --- 5. claude_prompt ---
	if err := conn.WriteJSON(map[string]any{
		"type": "claude_prompt", "sessionID": sid,
		"text": "Reply with the single word: ping",
	}); err != nil {
		return err
	}

	// Expect a stream of claude_event frames. We want to see:
	//   - at least one system event (init)
	//   - a result event eventually
	// regardless of whether the model actually answered (if creds
	// are missing, the result will be an error frame; that's fine
	// for verifying the pipeline).
	var sawSystem, sawResult bool
	var resultPayload struct {
		IsError      bool    `json:"is_error"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Result       string  `json:"result"`
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && !sawResult {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return fmt.Errorf("read after claude_prompt: %w", err)
		}
		if m.SessionID != "" && m.SessionID != sid {
			continue
		}
		if m.Type == "error" {
			return fmt.Errorf("server error: code=%s msg=%s", m.Code, m.Message)
		}
		if m.Type != "claude_event" {
			continue
		}
		switch m.EventKind {
		case "system":
			sawSystem = true
		case "result":
			sawResult = true
			_ = json.Unmarshal(m.Payload, &resultPayload)
		}
	}
	if !sawSystem {
		return fmt.Errorf("never saw a system event")
	}
	if !sawResult {
		return fmt.Errorf("never saw a result event")
	}
	if resultPayload.IsError {
		// This is the expected branch when credentials are missing.
		// We still passed: the pipeline shipped a valid result with
		// is_error=true. Print a note.
		fmt.Printf("✓ result frame received (is_error=true: %s)\n",
			truncOneLine(resultPayload.Result, 80))
	} else {
		fmt.Printf("✓ result frame received: total=%.4f result=%q\n",
			resultPayload.TotalCostUSD,
			truncOneLine(resultPayload.Result, 60))
	}
	return nil
}

func login() (string, error) {
	body, _ := json.Marshal(map[string]string{"user": "admin", "password": "admin"})
	resp, err := http.Post(base+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Token, nil
}

func wipeSessions(tok string) {
	req, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	var list []struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&list)
	for _, s := range list {
		dr, _ := http.NewRequest("DELETE", base+"/api/sessions/"+s.ID, nil)
		dr.Header.Set("Authorization", "Bearer "+tok)
		r, _ := http.DefaultClient.Do(dr)
		if r != nil {
			r.Body.Close()
		}
	}
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
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.ID, nil
}

// waitFor reads until the predicate returns true for a frame whose
// sessionID matches sid (or is empty).
func waitFor(conn *websocket.Conn, sid string, pred func(wsMsg) bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return err
		}
		if m.SessionID != "" && m.SessionID != sid {
			continue
		}
		if pred(m) {
			return nil
		}
	}
	return fmt.Errorf("timeout")
}

func truncOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
