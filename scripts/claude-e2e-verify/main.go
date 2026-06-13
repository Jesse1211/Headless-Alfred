// claude-e2e-verify mirrors EVERY HTTP / WS call the React frontend
// makes when a user runs through the MVP user-story:
//
//   1. Page load:        GET /, GET /assets/*, GET /api/sessions (with token)
//   2. Create session:   POST /api/sessions
//   3. Open WS:          GET /ws?token=... (Upgrade)
//   4. Server pushes:    idle (with mode field)
//   5. Type "/claude":   client sends enter_claude
//   6. Server:           claude_entered + stream of pty_data
//   7. Client decodes pty_data, verifies it looks like Claude's TUI
//      (welcome banner OR /login screen — anything but a hang).
//   8. Click "Exit Claude": client sends exit_claude
//   9. Server:           claude_exited
//  10. Multi-session sanity: shell mode of OTHER session still works
//      (run "echo hello" → started/chunk/done with "hello" in output).
//
// Run with: go run ./scripts/claude-e2e-verify
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
	CmdID     string `json:"cmdId,omitempty"`
	Data      string `json:"data,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Mode      string `json:"mode,omitempty"`
	ExitCode  int    `json:"exitCode,omitempty"`
}

type check struct {
	name string
	pass bool
	note string
}

func main() {
	var checks []check
	if err := run(&checks); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	all := true
	for _, c := range checks {
		mark := "✓"
		if !c.pass {
			mark = "✗"
			all = false
		}
		fmt.Printf("%s %s %s\n", mark, c.name, c.note)
	}
	if !all {
		os.Exit(1)
	}
	fmt.Println("\nALL CHECKS PASSED — frontend bundle + backend flow are wired correctly.")
}

func run(checks *[]check) error {
	// === Phase 1: Page-load HTTP path ===
	tok, err := login()
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	*checks = append(*checks, check{"login: admin/admin → token", true, ""})

	// Wipe any leftover sessions so we control state.
	if err := wipeSessions(tok); err != nil {
		return fmt.Errorf("wipe: %w", err)
	}

	// Pretend to be the React app on first paint: GET /, GET / api/sessions.
	if code, _ := httpGetCode("/", ""); code != 200 {
		*checks = append(*checks, check{"GET /", false, fmt.Sprintf("code=%d", code)})
	} else {
		*checks = append(*checks, check{"GET /", true, ""})
	}
	if code, body := httpGetCode("/api/sessions", tok); code != 200 || !strings.HasPrefix(strings.TrimSpace(body), "[") {
		*checks = append(*checks, check{"GET /api/sessions (empty)", false, fmt.Sprintf("code=%d body=%q", code, body)})
	} else {
		*checks = append(*checks, check{"GET /api/sessions (empty list)", true, ""})
	}

	// Verify the served JS bundle has every claude-mode string we expect.
	if ok, note := verifyBundleStrings(); ok {
		*checks = append(*checks, check{"frontend bundle has claude-mode wiring", true, ""})
	} else {
		*checks = append(*checks, check{"frontend bundle has claude-mode wiring", false, note})
	}

	// === Phase 2: Create two sessions ===
	sidA, err := createSession(tok, "session-A")
	if err != nil {
		return err
	}
	sidB, err := createSession(tok, "session-B")
	if err != nil {
		return err
	}
	*checks = append(*checks, check{"create two sessions", true, fmt.Sprintf("A=%s B=%s", sidA[:6], sidB[:6])})

	// === Phase 3: Open WS — same as ShellSocket does ===
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(),
		"ws://127.0.0.1:18080/ws?token="+tok, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// Expect: two idle frames, one per session, each with mode field.
	idleSeen := map[string]string{}
	deadline := time.Now().Add(3 * time.Second)
	for len(idleSeen) < 2 && time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		if m.Type == "idle" {
			idleSeen[m.SessionID] = m.Mode
		}
	}
	if len(idleSeen) >= 2 && idleSeen[sidA] == "shell" && idleSeen[sidB] == "shell" {
		*checks = append(*checks, check{"WS sends idle{mode:shell} for both sessions", true, ""})
	} else {
		*checks = append(*checks, check{"WS sends idle{mode:shell} for both sessions", false, fmt.Sprintf("got=%v", idleSeen)})
	}

	// === Phase 4: Shell mode still works (session A: echo hello) ===
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sidA, "command": "echo hello-from-shell"}); err != nil {
		return err
	}
	gotChunk := false
	gotDone := false
	deadline = time.Now().Add(8 * time.Second)
	var shellOut bytes.Buffer
	for time.Now().Before(deadline) && !gotDone {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		if m.SessionID != sidA {
			continue
		}
		if m.Type == "chunk" {
			gotChunk = true
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			shellOut.Write(b)
		}
		if m.Type == "done" {
			gotDone = true
		}
	}
	if gotChunk && gotDone && strings.Contains(shellOut.String(), "hello-from-shell") {
		*checks = append(*checks, check{"shell mode: run/chunk/done still works (session A)", true, ""})
	} else {
		*checks = append(*checks, check{"shell mode: run/chunk/done still works (session A)", false,
			fmt.Sprintf("chunk=%v done=%v out=%q", gotChunk, gotDone, shellOut.String())})
	}

	// === Phase 5: Enter Claude mode in session B (the MVP path) ===
	if err := conn.WriteJSON(map[string]any{"type": "enter_claude", "sessionID": sidB}); err != nil {
		return err
	}
	gotEntered := false
	var ptyData bytes.Buffer
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		if m.SessionID != "" && m.SessionID != sidB {
			continue
		}
		if m.Type == "error" {
			return fmt.Errorf("server error after enter_claude: %s: %s", m.Code, m.Message)
		}
		if m.Type == "claude_entered" {
			gotEntered = true
		}
		if m.Type == "pty_data" {
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			ptyData.Write(b)
			if ptyData.Len() > 1500 {
				break
			}
		}
	}
	if gotEntered {
		*checks = append(*checks, check{"enter_claude → claude_entered", true, ""})
	} else {
		*checks = append(*checks, check{"enter_claude → claude_entered", false, ""})
	}

	// === Phase 6: Verify pty_data looks like Claude TUI ===
	ptyText := ptyData.String()
	hasESC := strings.Contains(ptyText, "\x1b")
	mentionsClaude := strings.Contains(ptyText, "Claude") || strings.Contains(ptyText, "claude")
	mentionsLogin := strings.Contains(ptyText, "login") ||
		strings.Contains(ptyText, "Sign in") ||
		strings.Contains(ptyText, "API key") ||
		strings.Contains(ptyText, "Welcome")
	if hasESC && mentionsClaude {
		note := fmt.Sprintf("%d bytes, mentions Claude=%v login-screen=%v", ptyData.Len(), mentionsClaude, mentionsLogin)
		*checks = append(*checks, check{"pty_data carries Claude TUI bytes", true, note})
	} else {
		*checks = append(*checks, check{"pty_data carries Claude TUI bytes", false,
			fmt.Sprintf("hasESC=%v mentionsClaude=%v len=%d first200=%q",
				hasESC, mentionsClaude, ptyData.Len(), trunc(ptyText, 200))})
	}

	// === Phase 7: While in Claude mode, the OTHER session is still
	// shell mode and accepts a run command (multi-session sanity). ===
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sidA, "command": "echo still-works"}); err != nil {
		return err
	}
	gotAOut := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !gotAOut {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		if m.SessionID != sidA {
			continue
		}
		if m.Type == "chunk" {
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			if strings.Contains(string(b), "still-works") {
				gotAOut = true
			}
		}
	}
	if gotAOut {
		*checks = append(*checks, check{"multi-session: session A (shell) works while B is claude", true, ""})
	} else {
		*checks = append(*checks, check{"multi-session: session A (shell) works while B is claude", false, "no chunk with 'still-works'"})
	}

	// === Phase 8: Exit Claude ===
	if err := conn.WriteJSON(map[string]any{"type": "exit_claude", "sessionID": sidB}); err != nil {
		return err
	}
	gotExited := false
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		if m.Type == "claude_exited" && m.SessionID == sidB {
			gotExited = true
			break
		}
	}
	if gotExited {
		*checks = append(*checks, check{"exit_claude → claude_exited", true, ""})
	} else {
		*checks = append(*checks, check{"exit_claude → claude_exited", false, ""})
	}

	// === Phase 9: After exit, session B is back to shell — run a
	// simple command to prove the bash prompt is alive.
	// Give bash a moment to settle after SIGKILL of claude.
	// Clear the prior ReadDeadline first — gorilla websocket treats an
	// expired deadline as a fatal connection error. ===
	_ = conn.SetReadDeadline(time.Time{})
	time.Sleep(500 * time.Millisecond)
	if err := conn.WriteJSON(map[string]any{"type": "run", "sessionID": sidB, "command": "echo back-in-shell"}); err != nil {
		return err
	}
	gotBackShell := false
	deadline = time.Now().Add(8 * time.Second)
	var phase9Frames []string
	for time.Now().Before(deadline) && !gotBackShell {
		conn.SetReadDeadline(deadline)
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		if m.SessionID != sidB {
			continue
		}
		phase9Frames = append(phase9Frames, fmt.Sprintf("type=%s code=%s", m.Type, m.Code))
		if m.Type == "chunk" {
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			if strings.Contains(string(b), "back-in-shell") {
				gotBackShell = true
			}
		}
	}
	if !gotBackShell {
		fmt.Printf("  [debug] phase 9 frames: %v\n", phase9Frames)
	}
	if gotBackShell {
		*checks = append(*checks, check{"after exit_claude, session B is back in shell mode", true, ""})
	} else {
		*checks = append(*checks, check{"after exit_claude, session B is back in shell mode", false,
			"bash didn't echo back-in-shell — claude may still be holding the PTY"})
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

func wipeSessions(tok string) error {
	req, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var list []struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&list)
	for _, s := range list {
		dr, _ := http.NewRequest("DELETE", base+"/api/sessions/"+s.ID, nil)
		dr.Header.Set("Authorization", "Bearer "+tok)
		resp2, _ := http.DefaultClient.Do(dr)
		if resp2 != nil {
			resp2.Body.Close()
		}
	}
	return nil
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
		return "", fmt.Errorf("create %s status=%d", name, resp.StatusCode)
	}
	var out struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID, nil
}

func httpGetCode(path, tok string) (int, string) {
	req, _ := http.NewRequest("GET", base+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

// verifyBundleStrings curls the served React bundle and looks for the
// strings the frontend would emit if the claude-mode code is actually
// shipped. Cheaper than spinning up a headless browser.
func verifyBundleStrings() (bool, string) {
	resp, err := http.Get(base + "/")
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	htmlBuf := new(bytes.Buffer)
	htmlBuf.ReadFrom(resp.Body)
	html := htmlBuf.String()

	// Extract bundle name like index-XYZ.js.
	idx := strings.Index(html, "/assets/index-")
	if idx < 0 {
		return false, "no index- bundle in HTML"
	}
	end := strings.Index(html[idx:], ".js")
	if end < 0 {
		return false, "bundle name not closed"
	}
	bundlePath := html[idx : idx+end+3]
	resp2, err := http.Get(base + bundlePath)
	if err != nil {
		return false, err.Error()
	}
	defer resp2.Body.Close()
	jsBuf := new(bytes.Buffer)
	jsBuf.ReadFrom(resp2.Body)
	js := jsBuf.String()

	required := []string{
		"enter_claude", "exit_claude", "stdin",
		"claude_entered", "claude_exited", "pty_data",
		"Exit Claude", "/claude",
		"xterm", // xterm.js lib reference
	}
	missing := []string{}
	for _, s := range required {
		if !strings.Contains(js, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Sprintf("missing in bundle: %v", missing)
	}
	return true, fmt.Sprintf("bundle %s has all %d markers", bundlePath, len(required))
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\x1b", "ESC")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
