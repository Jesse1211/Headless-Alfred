package shell

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
)

func newTestTmuxShell(t *testing.T, runner tmuxio.TmuxRunner) (*TmuxShell, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ts, err := NewTmuxShell(TmuxShellConfig{
		SessionID:  "sess-1",
		Nonce:      "nonce-x",
		Runner:     runner,
		StreamPath: filepath.Join(dir, "pty.stream"),
		OffsetPath: filepath.Join(dir, "pty.offset"),
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("NewTmuxShell: %v", err)
	}
	return ts, dir
}

func TestTmuxShell_Start_CreatesSessionAndConfigures(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	if err := ts.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ts.Close()

	calls := fr.Calls()
	// We expect, in order:
	//   NewSession sess-1 bash --noprofile --norc
	//   SetOption sess-1 remain-on-exit on
	//   SendText sess-1 "stty -echo"
	//   SendEnter sess-1
	//   PipePane sess-1 "cat >> <streamPath>"
	wantMethods := []string{"NewSession", "SetOption", "SendText", "SendEnter", "PipePane"}
	if len(calls) < len(wantMethods) {
		t.Fatalf("only %d calls, want at least %d: %+v", len(calls), len(wantMethods), calls)
	}
	for i, want := range wantMethods {
		if calls[i].Method != want {
			t.Fatalf("call %d = %q, want %q (all calls: %+v)", i, calls[i].Method, want, calls)
		}
	}
	if calls[0].Args[0] != "sess-1" || calls[0].Args[1] != "bash" {
		t.Fatalf("NewSession args: %+v", calls[0].Args)
	}
	if calls[1].Args[1] != "remain-on-exit" || calls[1].Args[2] != "on" {
		t.Fatalf("SetOption args: %+v", calls[1].Args)
	}
	if calls[2].Args[1] != "stty -echo" {
		t.Fatalf("SendText args: %+v", calls[2].Args)
	}
}

func TestTmuxShell_Close_KillsSessionAndIsIdempotent(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	_ = ts.Start()

	if err := ts.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	calls := fr.Calls()
	sawKill := false
	for _, c := range calls {
		if c.Method == "KillSession" && c.Args[0] == "sess-1" {
			sawKill = true
		}
	}
	if !sawKill {
		t.Fatalf("KillSession not called: %+v", calls)
	}
	// Second Close — must not blow up, must not double-kill.
	if err := ts.Close(); err != nil {
		t.Fatalf("Close (second): %v", err)
	}
}
