//go:build e2e

package e2e

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestE2E_RenamePersistsAcrossReload(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "")
	body := []byte(`{"name":"training"}`)
	req, _ := http.NewRequest("PATCH", baseHTTP+"/api/sessions/"+sid, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-For", testIP(t))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("rename code = %d", resp.StatusCode)
	}
	restartAlfredProcess(t)
	tok2, _ := login(t, testUser, testPassword)
	listing := getJSON(t, tok2, "/api/sessions")
	if !strings.Contains(string(listing), "training") {
		t.Fatalf("rename did not survive restart: %s", listing)
	}
}
