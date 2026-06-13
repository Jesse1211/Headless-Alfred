// claude-mode-smoke verifies the bare backend flow:
//
//  1. log in, create a session, dial WS
//  2. enter_claude: expect claude_entered, then pty_data with bytes
//     resembling claude's startup output ("Please run /login" since
//     we have no API key configured)
//  3. exit_claude: expect claude_exited
//
// Headless. Does NOT actually talk to Anthropic.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	base = "http://127.0.0.1:18080"
	user = "admin"
	pass = "admin"
)

type wsMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID,omitempty"`
	Data      string `json:"data,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Mode      string `json:"mode,omitempty"`
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
	sid, err := createSession(tok)
	if err != nil {
		return err
	}
	fmt.Printf("session=%s\n", sid)

	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(),
		"ws://127.0.0.1:18080/ws?token="+tok, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Step 1: enter_claude.
	if err := conn.WriteJSON(map[string]any{
		"type": "enter_claude", "sessionID": sid,
	}); err != nil {
		return err
	}
	fmt.Println("→ enter_claude")

	// Step 2: wait for claude_entered + first pty_data with some text.
	gotEntered := false
	var ptyAccum []byte
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if m.SessionID != "" && m.SessionID != sid {
			continue
		}
		switch m.Type {
		case "claude_entered":
			gotEntered = true
			fmt.Println("✓ claude_entered")
		case "pty_data":
			data, _ := base64.StdEncoding.DecodeString(m.Data)
			ptyAccum = append(ptyAccum, data...)
			if len(ptyAccum) > 100 && bytes.ContainsAny(ptyAccum, "/") {
				fmt.Printf("✓ got %d bytes of pty_data (first 200): %q\n",
					len(ptyAccum), trunc(ptyAccum, 200))
				goto enteredOK
			}
		case "error":
			return fmt.Errorf("server error: %s: %s", m.Code, m.Message)
		}
	}
	if !gotEntered {
		return fmt.Errorf("never got claude_entered")
	}
	return fmt.Errorf("got claude_entered but not enough pty_data (%d bytes: %q)",
		len(ptyAccum), ptyAccum)

enteredOK:
	// Step 3: send a tiny stdin byte (down arrow).
	stdinPayload := base64.StdEncoding.EncodeToString([]byte("\x1b[B"))
	if err := conn.WriteJSON(map[string]any{
		"type": "stdin", "sessionID": sid, "data": stdinPayload,
	}); err != nil {
		return err
	}
	fmt.Println("→ stdin (down arrow)")

	// Step 4: exit_claude.
	if err := conn.WriteJSON(map[string]any{
		"type": "exit_claude", "sessionID": sid,
	}); err != nil {
		return err
	}
	fmt.Println("→ exit_claude")
	deadline = time.Now().Add(15 * time.Second)
	totalPtyAfter := 0
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return fmt.Errorf("read: %w (got %d pty_data bytes after exit)", err, totalPtyAfter)
		}
		switch m.Type {
		case "pty_data":
			data, _ := base64.StdEncoding.DecodeString(m.Data)
			totalPtyAfter += len(data)
		case "claude_exited":
			if m.SessionID == sid {
				fmt.Printf("✓ claude_exited (drained %d more pty bytes en route)\n", totalPtyAfter)
				return nil
			}
		default:
			fmt.Printf("  recv: type=%s sid=%s code=%s\n", m.Type, m.SessionID, m.Code)
		}
	}
	return fmt.Errorf("never got claude_exited (drained %d pty bytes)", totalPtyAfter)
}

func login() (string, error) {
	body, _ := json.Marshal(map[string]string{"user": user, "password": pass})
	resp, err := http.Post(base+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token, nil
}

func createSession(tok string) (string, error) {
	body, _ := json.Marshal(map[string]string{"name": "claude-smoke"})
	req, _ := http.NewRequest("POST", base+"/api/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		b, _ := readBody(resp)
		return "", fmt.Errorf("create %d: %s", resp.StatusCode, b)
	}
	var out struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID, nil
}

func readBody(r *http.Response) (string, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	return buf.String(), err
}

func trunc(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		s = s[:n] + "…"
	}
	return strings.ReplaceAll(s, "\x1b", "ESC")
}
