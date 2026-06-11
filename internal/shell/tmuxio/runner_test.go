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

func TestFakeRunner_RecordsCalls(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("s1", "bash", "--noprofile", "--norc")
	_ = f.SendText("s1", "echo hello")
	_ = f.SendEnter("s1")
	_ = f.SetOption("s1", "remain-on-exit", "on")
	_ = f.PipePane("s1", "cat >> /tmp/x")

	got := f.Calls()
	if len(got) != 5 {
		t.Fatalf("want 5 calls, got %d: %+v", len(got), got)
	}
	if got[0].Method != "NewSession" || got[0].Args[0] != "s1" {
		t.Fatalf("call 0 = %+v", got[0])
	}
	if got[1].Method != "SendText" || got[1].Args[1] != "echo hello" {
		t.Fatalf("call 1 = %+v", got[1])
	}
	if got[2].Method != "SendEnter" || got[2].Args[0] != "s1" {
		t.Fatalf("call 2 = %+v", got[2])
	}
	if got[3].Method != "SetOption" || got[3].Args[2] != "on" {
		t.Fatalf("call 3 = %+v", got[3])
	}
}

func TestFakeRunner_ListSessions_Default_Empty(t *testing.T) {
	f := NewFakeRunner()
	names, err := f.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty, got %v", names)
	}
}

func TestFakeRunner_NewSession_AddsToListSessions(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	_ = f.NewSession("beta", "bash")
	names, _ := f.ListSessions()
	if len(names) != 2 {
		t.Fatalf("want 2, got %v", names)
	}
}

func TestFakeRunner_KillSession_RemovesFromList(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	_ = f.KillSession("alpha")
	names, _ := f.ListSessions()
	if len(names) != 0 {
		t.Fatalf("want empty, got %v", names)
	}
}

func TestFakeRunner_KillSession_NonExistent_Idempotent(t *testing.T) {
	f := NewFakeRunner()
	if err := f.KillSession("ghost"); err != nil {
		t.Fatalf("kill ghost: %v", err)
	}
}

func TestFakeRunner_PanePID_DefaultsToSyntheticValue(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	pid, err := f.PanePID("alpha")
	if err != nil {
		t.Fatalf("PanePID: %v", err)
	}
	if pid == 0 {
		t.Fatalf("expected non-zero synthetic pid")
	}
}

func TestFakeRunner_PaneDeadFlag(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	dead, _ := f.PaneDead("alpha")
	if dead {
		t.Fatal("fresh session should not have dead pane")
	}
	f.MarkPaneDead("alpha")
	dead, _ = f.PaneDead("alpha")
	if !dead {
		t.Fatal("after MarkPaneDead, expected pane_dead=1")
	}
}

func TestFakeRunner_ErrorInjection(t *testing.T) {
	f := NewFakeRunner()
	f.NextErr("NewSession", errInjected)
	if err := f.NewSession("a", "bash"); err != errInjected {
		t.Fatalf("expected injected error, got %v", err)
	}
	// Error is one-shot.
	if err := f.NewSession("a", "bash"); err != nil {
		t.Fatalf("subsequent call should not error, got %v", err)
	}
}

var errInjected = errSentinel("injected")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
