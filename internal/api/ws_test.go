package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/session"
)

func dialWS(t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	dialURL := strings.Replace(url, "http://", "ws://", 1) + "/ws?token=" + token
	h := http.Header{}
	c, _, err := websocket.DefaultDialer.Dial(dialURL, h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func setupWSServerMulti(t *testing.T) (string, *session.Manager) {
	t.Helper()
	m := newTestManager(t)
	a := auth.Auth{User: "admin", Password: "pw", Token: "tok"}
	rl := auth.NewRateLimiter(5, time.Minute)
	r := NewRouter(Deps{
		Manager:     m,
		Auth:        a,
		RateLimiter: rl,
		Ready:       func() bool { return true },
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL, m
}

func TestWS_OnConnect_SendsIdleForEverySession(t *testing.T) {
	url, m := setupWSServerMulti(t)
	a, _ := m.Create("A")
	b, _ := m.Create("B")
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	seen := map[string]bool{}
	endBy := time.Now().Add(3 * time.Second)
	for len(seen) < 2 && time.Now().Before(endBy) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var msg OutMsg
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		if msg.Type == "idle" {
			seen[msg.SessionID] = true
		}
	}
	if !seen[a.ID] || !seen[b.ID] {
		t.Fatalf("missing idle: %+v", seen)
	}
}

func TestWS_RunWithUnknownSessionID_ReturnsError(t *testing.T) {
	// No sessions exist, so server sends nothing on connect.
	// Send run with unknown sessionID immediately and expect an error back.
	url, _ := setupWSServerMulti(t)
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	_ = conn.WriteJSON(InMsg{Type: "run", SessionID: "nope", Command: "ls"})
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg OutMsg
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg.Type != "error" || msg.Code != "unknown_session" {
		t.Fatalf("expected error/unknown_session, got %+v", msg)
	}
}

func TestWS_SessionClosed_BroadcastsToConnectedClients(t *testing.T) {
	url, m := setupWSServerMulti(t)
	sess, _ := m.Create("A")
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for i := 0; i < 5; i++ {
		var m2 OutMsg
		if err := conn.ReadJSON(&m2); err != nil {
			break
		}
		if m2.Type == "idle" && m2.SessionID == sess.ID {
			break
		}
	}
	_ = m.Close(sess.ID)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var m2 OutMsg
		if err := conn.ReadJSON(&m2); err != nil {
			t.Fatalf("never received session_closed: %v", err)
		}
		if m2.Type == "session_closed" && m2.SessionID == sess.ID {
			return
		}
	}
}

func TestWS_SessionRenamed_BroadcastsToConnectedClients(t *testing.T) {
	url, m := setupWSServerMulti(t)
	sess, _ := m.Create("A")
	conn := dialWS(t, url, "tok")
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for i := 0; i < 5; i++ {
		var m2 OutMsg
		if err := conn.ReadJSON(&m2); err != nil {
			break
		}
		if m2.Type == "idle" {
			break
		}
	}
	_ = m.Rename(sess.ID, "training")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var m2 OutMsg
		if err := conn.ReadJSON(&m2); err != nil {
			t.Fatalf("never received session_renamed: %v", err)
		}
		if m2.Type == "session_renamed" && m2.SessionID == sess.ID && m2.Name == "training" {
			return
		}
	}
}

var _ = base64.StdEncoding
var _ = json.Marshal
