package tmuxio

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func tmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// shortSocketPath returns a tmux socket path short enough for UNIX
// domain socket limits (~104 bytes on macOS). t.TempDir is too deep.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "tmux-*.sock")
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

func TestExecRunner_ListSessions_NoServer_ReturnsEmpty(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux binary not on PATH")
	}
	// Use a brand-new socket path — guaranteed no server is running.
	// On a tmux-less server the error stderr is one of
	//   "no server running on /..."
	//   "error connecting to /..."
	// We treat both as "0 sessions, no error".
	sock := shortSocketPath(t)
	r := NewExecRunner(sock)
	sessions, err := r.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions on a missing-server should not error, got %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty, got %+v", sessions)
	}
}

func TestExecRunner_CreateAndListSession_RoundTrip(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux binary not on PATH")
	}
	sock := shortSocketPath(t)
	r := NewExecRunner(sock)
	t.Cleanup(func() {
		_ = r.KillSession("integration-test")
	})
	if err := r.NewSession("integration-test", "bash", "--noprofile", "--norc"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessions, err := r.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s == "integration-test" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("integration-test not in %v", sessions)
	}
}

func TestExecRunner_SendTextThenEnter_ExecutesCommand(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux binary not on PATH")
	}
	sock := shortSocketPath(t)
	r := NewExecRunner(sock)
	t.Cleanup(func() { _ = r.KillSession("exec-test") })
	if err := r.NewSession("exec-test", "bash", "--noprofile", "--norc"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Capture pane output to a temp file via pipe-pane.
	outFile := t.TempDir() + "/out"
	if err := r.PipePane("exec-test", "cat >> "+outFile); err != nil {
		t.Fatalf("PipePane: %v", err)
	}
	if err := r.SendText("exec-test", "echo HELLO-WORLD"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if err := r.SendEnter("exec-test"); err != nil {
		t.Fatalf("SendEnter: %v", err)
	}
	// bash needs a tick to execute and tmux a tick to flush. Poll up to 2s.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(outFile)
		if strings.Contains(string(data), "HELLO-WORLD") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(outFile)
	t.Fatalf("HELLO-WORLD never appeared in pane output. got: %q", data)
}
