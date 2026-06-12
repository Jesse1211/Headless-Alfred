//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestE2E_SessionLimit creates 8 sessions and verifies the 9th POST is
// rejected with HTTP 422 and {"code":"session_limit"}.
//
// Note: this test reaches the limit by creating sessions, so it should run
// against a fresh cluster (or be the first test in a run). The teardown of
// previous tests does not necessarily delete every session.
func TestE2E_SessionLimit(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	for i := 0; i < 8; i++ {
		_ = createSession(t, tok, "")
	}
	// 9th must be rejected with 422 session_limit.
	req, _ := http.NewRequest("POST", baseHTTP+"/api/sessions", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var msg map[string]any
	_ = json.Unmarshal(body, &msg)
	if code, _ := msg["code"].(string); code != "session_limit" {
		t.Fatalf("expected code session_limit, got body=%s", body)
	}
	if !strings.Contains(string(body), "session_limit") {
		t.Fatalf("body missing session_limit: %s", body)
	}
}
