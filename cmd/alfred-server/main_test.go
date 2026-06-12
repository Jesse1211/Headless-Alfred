package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var skipBootTests bool

func init() {
	if _, err := exec.LookPath("tmux"); err != nil {
		skipBootTests = true
	}
}

// TestBoot_StartsAndServesHealthz runs the binary in-process by
// directly invoking the boot sequence — main() with overridden env
// vars and a dynamic port.
func TestBoot_StartsAndServesHealthz(t *testing.T) {
	if skipBootTests {
		t.Skip("tmux binary not on PATH")
	}
	// Use a SHORT temp directory under /tmp so the macOS UNIX socket
	// path stays under the 104-byte limit. t.TempDir() returns
	// /var/folders/... which is too deep.
	dir, err := os.MkdirTemp("", "alfred-boot-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Choose a free port.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()

	// Mandatory env vars for auth.FromEnv to succeed.
	t.Setenv("ALFRED_USER", "admin")
	t.Setenv("ALFRED_PASSWORD", "pw")
	t.Setenv("ALFRED_TOKEN", "tok")
	t.Setenv("ALFRED_DATA_DIR", dir)
	t.Setenv("ALFRED_ADDR", addr)

	// Seed legacy commands so MigrateLegacyLayout has something to do.
	legacyCmds := filepath.Join(dir, "commands")
	_ = os.MkdirAll(legacyCmds, 0o700)
	_ = os.WriteFile(filepath.Join(legacyCmds, "01HZ.json"), []byte(`{"id":"01HZ","command":"ls","status":"completed","started_at":"2026-06-10T00:00:00Z"}`), 0o600)

	done := make(chan struct{})
	go func() {
		main()
		close(done)
	}()
	t.Cleanup(func() {
		// Best-effort shutdown: send SIGTERM to self. main() listens for it.
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})

	// Poll /healthz until ready (or timeout).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			// Also verify sessions.json was written by the migration.
			info, err := os.Stat(filepath.Join(dir, "sessions.json"))
			if err != nil || info.Size() == 0 {
				t.Fatalf("sessions.json missing or empty: %v", err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("server did not become ready within 5 seconds")
}

var _ = context.Background
