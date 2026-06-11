//go:build !windows

package shell

import (
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
}

func newTestShell(t *testing.T) *Shell {
	t.Helper()
	requireBash(t)
	s := NewShell(slog.Default())
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func runAndCollect(t *testing.T, s *Shell, cmdID, userCmd string) (string, int) {
	t.Helper()
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()

	if err := s.Write(cmdID, userCmd); err != nil {
		t.Fatalf("write: %v", err)
	}

	var output strings.Builder
	deadline := time.After(15 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Chunk != nil && evt.Chunk.CmdID == cmdID {
				output.Write(evt.Chunk.Bytes)
			}
			if evt.Ended != nil && evt.Ended.CmdID == cmdID {
				return output.String(), evt.Ended.ExitCode
			}
		case <-deadline:
			t.Fatalf("timeout waiting for cmd to end. partial output: %q", output.String())
		}
	}
}

func TestIntegration_EchoReturnsOutputAndZeroExit(t *testing.T) {
	s := newTestShell(t)
	out, ec := runAndCollect(t, s, "cmd-echo", "echo hello-world")
	if ec != 0 {
		t.Fatalf("exit code = %d, want 0", ec)
	}
	if !strings.Contains(out, "hello-world") {
		t.Fatalf("output missing expected text: %q", out)
	}
}

func TestIntegration_NonZeroExitCodePropagates(t *testing.T) {
	s := newTestShell(t)
	_, ec := runAndCollect(t, s, "cmd-fail", "false")
	if ec == 0 {
		t.Fatalf("expected non-zero exit, got 0")
	}
}

func TestIntegration_CDPersistsAcrossCommands(t *testing.T) {
	s := newTestShell(t)
	runAndCollect(t, s, "cd-cmd", "cd /tmp")
	out, ec := runAndCollect(t, s, "pwd-cmd", "pwd")
	if ec != 0 {
		t.Fatalf("pwd failed: ec=%d out=%q", ec, out)
	}
	if !strings.Contains(out, "/tmp") {
		t.Fatalf("pwd output = %q, want contains /tmp", out)
	}
}

func TestIntegration_CWDCapturedFromStartSentinel(t *testing.T) {
	s := newTestShell(t)
	runAndCollect(t, s, "cd-cmd", "cd /tmp")

	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("after-cd", "echo done"); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Started != nil && evt.Started.CmdID == "after-cd" {
				if evt.Started.Cwd != "/tmp" {
					t.Fatalf("captured cwd = %q, want /tmp", evt.Started.Cwd)
				}
				return
			}
		case <-deadline:
			t.Fatalf("no started event")
		}
	}
}

func TestIntegration_ConcurrentWriteRejectedAsBusy(t *testing.T) {
	s := newTestShell(t)
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("slow", "sleep 1; echo done"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.Write("second", "echo nope"); err != ErrBusy {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	// Drain until slow ends.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Ended != nil && evt.Ended.CmdID == "slow" {
				return
			}
		case <-deadline:
			t.Fatalf("slow never ended")
		}
	}
}

func TestIntegration_StopSendsSIGINT(t *testing.T) {
	s := newTestShell(t)
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("long", "sleep 30; echo done"); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Give bash a moment to actually start sleep.
	time.Sleep(200 * time.Millisecond)
	s.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Ended != nil && evt.Ended.CmdID == "long" {
				if evt.Ended.ExitCode == 0 {
					t.Fatalf("stop did not produce non-zero exit code")
				}
				return
			}
		case <-deadline:
			t.Fatalf("stop did not terminate command")
		}
	}
}

func TestIntegration_ShellUsableAfterStop(t *testing.T) {
	// Regression: a previous version of Stop killed bash and skipped restart,
	// permanently bricking the shell after a single Stop. Stop must leave the
	// shell ready to accept the next command.
	s := newTestShell(t)
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("first", "sleep 30"); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	// Drain until the first command's Ended event.
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case evt := <-sub.C:
			if evt.Ended != nil && evt.Ended.CmdID == "first" {
				break loop
			}
		case <-deadline:
			t.Fatalf("first command never ended")
		}
	}

	// Bash should have restarted by now; give it a moment.
	time.Sleep(200 * time.Millisecond)

	// Subsequent command must succeed.
	out, ec := runAndCollect(t, s, "second", "echo after-stop")
	if ec != 0 {
		t.Fatalf("second command exit = %d, want 0; out = %q", ec, out)
	}
	if !strings.Contains(out, "after-stop") {
		t.Fatalf("second command output missing expected text: %q", out)
	}
}

func TestIntegration_StreamingOutputArrivesIncrementally(t *testing.T) {
	s := newTestShell(t)
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("stream", "for i in 1 2 3; do echo $i; sleep 0.4; done"); err != nil {
		t.Fatalf("write: %v", err)
	}
	var chunkTimes []time.Time
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case evt := <-sub.C:
			if evt.Chunk != nil && evt.Chunk.CmdID == "stream" {
				chunkTimes = append(chunkTimes, time.Now())
			}
			if evt.Ended != nil && evt.Ended.CmdID == "stream" {
				break loop
			}
		case <-deadline:
			t.Fatalf("timeout. got %d chunks", len(chunkTimes))
		}
	}
	if len(chunkTimes) < 2 {
		t.Fatalf("expected multiple chunks across time, got %d", len(chunkTimes))
	}
	// At least one chunk should arrive >200ms after the first — i.e. they're
	// truly streamed, not delivered as a single end-of-command blob.
	gap := chunkTimes[len(chunkTimes)-1].Sub(chunkTimes[0])
	if gap < 200*time.Millisecond {
		t.Fatalf("chunks delivered too close together (%s); not streaming", gap)
	}
}
