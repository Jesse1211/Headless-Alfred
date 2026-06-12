//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestE2E_Migration_OldSchemaImported(t *testing.T) {
	// Seed the pod's /data with legacy layout, then restart alfred-server
	// (which runs migration on boot because sessions.json is absent).
	execInPod(t, `
		rm -f /data/sessions.json
		rm -rf /data/sessions
		mkdir -p /data/commands /data/outputs
		printf '%s' '{"id":"01HZA","command":"ls","status":"completed","started_at":"2026-06-10T10:00:00Z"}' > /data/commands/01HZA.json
		printf '%s' '{"id":"01HZB","command":"pwd","status":"completed","started_at":"2026-06-10T10:01:00Z"}' > /data/commands/01HZB.json
		printf 'tmp foo\n' > /data/outputs/01HZA.log
		printf '/tmp\n' > /data/outputs/01HZB.log
	`)
	restartAlfredProcess(t)

	tok, _ := login(t, testUser, testPassword)
	body := getJSON(t, tok, "/api/sessions")
	if !strings.Contains(string(body), "Imported") {
		t.Fatalf("Imported session missing: %s", body)
	}
	// Legacy dirs are gone.
	ls := execInPod(t, "ls /data 2>&1")
	if strings.Contains(ls, "commands") || strings.Contains(ls, "outputs") {
		t.Fatalf("legacy dirs still present: %s", ls)
	}
}
