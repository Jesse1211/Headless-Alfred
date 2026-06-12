//go:build integration
// +build integration

package shell

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
)

// shortIntegSocketPath returns a tmux socket path short enough for UNIX
// domain socket limits (~104 bytes on macOS). t.TempDir is too deep.
func shortIntegSocketPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "alfred-integ-*.sock")
	if err != nil {
		t.Fatalf("create temp socket path: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	// Tmux refuses to bind a socket that already exists.
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

func TestIntegration_TmuxShell_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH")
	}
	dir := t.TempDir()
	sock := shortIntegSocketPath(t)
	runner := tmuxio.NewExecRunner(sock)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ts, err := NewTmuxShell(TmuxShellConfig{
		SessionID:  "integ-1",
		Nonce:      "abc123",
		Runner:     runner,
		StreamPath: filepath.Join(dir, "pty.stream"),
		OffsetPath: filepath.Join(dir, "pty.offset"),
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("NewTmuxShell: %v", err)
	}
	if err := ts.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ts.Close()

	sub, cancel := ts.SubscribeEvents(16)
	defer cancel()

	if err := ts.Write("echo-1", `echo INTEG_OK`); err != nil {
		t.Fatalf("Write: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-sub.C:
			if ev.Ended != nil && ev.Ended.CmdID == "echo-1" {
				if ev.Ended.ExitCode != 0 {
					t.Fatalf("exit = %d, want 0", ev.Ended.ExitCode)
				}
				if !strings.Contains(string(ev.Ended.Output), "INTEG_OK") {
					t.Fatalf("output missing INTEG_OK: %q", ev.Ended.Output)
				}
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("timeout waiting for Ended event from real tmux")
}
