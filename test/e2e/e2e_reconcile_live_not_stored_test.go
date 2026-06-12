//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestE2E_Reconcile_LiveButNotStored_KillsOrphan(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	// Create an orphan tmux session directly via the socket. alfred-server
	// holds the socket at /data/alfred-tmux.sock.
	_ = execInPod(t,
		"tmux -S /data/alfred-tmux.sock new-session -d -s ghost-orphan bash --noprofile --norc")
	// Restart alfred-server so Reconcile runs.
	restartAlfredProcess(t)
	// The orphan must be gone.
	listing := execInPod(t, "tmux -S /data/alfred-tmux.sock ls 2>&1 || true")
	if strings.Contains(listing, "ghost-orphan") {
		t.Fatalf("orphan tmux session still alive after reconcile: %s", listing)
	}
	// And no leaked entry in /api/sessions.
	apiList := getJSON(t, tok, "/api/sessions")
	if strings.Contains(string(apiList), "ghost-orphan") {
		t.Fatalf("orphan leaked into /api/sessions: %s", apiList)
	}
}
