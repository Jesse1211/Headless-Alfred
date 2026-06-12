// multi-session-smoke spins up two sessions against the running alfred,
// runs commands in each, prints the raw persisted output verbatim, and
// flags any control-sequence / prompt residue.
//
// Run with: go run ./scripts/multi-session-smoke
package main

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
	"time"

	"github.com/gorilla/websocket"
)

const (
	base = "http://127.0.0.1:18080"
	user = "admin"
	pass = "e2etest"
)

type wsMsg struct {
	Type       string `json:"type"`
	SessionID  string `json:"sessionID,omitempty"`
	CmdID      string `json:"cmdId,omitempty"`
	Command    string `json:"command,omitempty"`
	Data       string `json:"data,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run() error {
	tok, err := login()
	if err != nil {
		return err
	}
	sidA, err := createSession(tok, "A")
	if err != nil {
		return err
	}
	sidB, err := createSession(tok, "B")
	if err != nil {
		return err
	}
	fmt.Printf("sessions: A=%s  B=%s\n\n", sidA, sidB)

	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(),
		"ws://127.0.0.1:18080/ws?token="+tok, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Run mkdir + ls + pwd in A; then ls in B to verify shared FS.
	mkdirCmd := fmt.Sprintf("mkdir -p /tmp/alf-test-%s && echo done", sidA[:6])
	if err := runCmd(conn, sidA, mkdirCmd); err != nil {
		return err
	}
	if err := runCmd(conn, sidA, "pwd"); err != nil {
		return err
	}
	if err := runCmd(conn, sidB, "ls /tmp | grep alf-test-"); err != nil {
		return err
	}

	// Now fetch persisted outputs of each cmd via REST and dump them with
	// any control chars/escape sequences clearly visible.
	fmt.Println("\n=== Persisted output dump ===")
	for _, sid := range []string{sidA, sidB} {
		cmds, err := listCommands(tok, sid)
		if err != nil {
			return err
		}
		for _, c := range cmds {
			full, err := getCommand(tok, sid, c.ID)
			if err != nil {
				return err
			}
			fmt.Printf("\n--- session=%s cmd=%q (%s) exit=%d ---\n", shortID(sid), full.Command, full.ID, full.ExitCode)
			fmt.Printf("output raw bytes (%d): %q\n", len(full.Output), full.Output)
			suspicious := checkResidue(full.Output)
			if len(suspicious) > 0 {
				fmt.Printf("RESIDUE: %v\n", suspicious)
			} else {
				fmt.Printf("output clean\n")
			}
		}
	}
	return nil
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
		b, _ := readAll(resp.Body)
		return "", fmt.Errorf("create %s: %d %s", name, resp.StatusCode, b)
	}
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID, nil
}

func runCmd(conn *websocket.Conn, sid, cmd string) error {
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sid, "command": cmd}); err != nil {
		return err
	}
	// Wait for done for this session.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return err
		}
		if m.SessionID != sid {
			continue
		}
		if m.Type == "done" {
			return nil
		}
		if m.Type == "error" {
			return fmt.Errorf("server error: %s %s", m.Code, m.Message)
		}
		_ = m
		_ = base64.StdEncoding
	}
	return fmt.Errorf("timeout waiting for done for %q", cmd)
}

type cmdSummary struct {
	ID string `json:"id"`
}

func listCommands(tok, sid string) ([]cmdSummary, error) {
	req, _ := http.NewRequest("GET", base+"/api/sessions/"+sid+"/commands", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []cmdSummary
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

type fullCmd struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func getCommand(tok, sid, cid string) (fullCmd, error) {
	req, _ := http.NewRequest("GET", base+"/api/sessions/"+sid+"/commands/"+cid, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fullCmd{}, err
	}
	defer resp.Body.Close()
	var out fullCmd
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

func checkResidue(s string) []string {
	var found []string
	if strings.Contains(s, "\x1b[?2004") {
		found = append(found, "bracketed-paste \\x1b[?2004")
	}
	if strings.Contains(s, "bash-") && strings.Contains(s, "$") {
		found = append(found, "bash prompt 'bash-X$'")
	}
	if strings.Contains(s, "\x1b[") {
		found = append(found, "other ANSI escape \\x1b[")
	}
	return found
}

func shortID(s string) string {
	if len(s) > 6 {
		return s[:6]
	}
	return s
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			return buf, nil
		}
	}
}

var _ = url.Parse
