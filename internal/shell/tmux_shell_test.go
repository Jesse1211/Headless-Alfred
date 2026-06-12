package shell

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestTmuxShell_Write_SendsWrapperThenEnter(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	_ = ts.Start()
	defer ts.Close()

	if err := ts.Write("cmd-1", "echo hi"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	calls := fr.Calls()
	// Find the last SendText + the SendEnter right after it.
	var lastSendText *tmuxio.Call
	var sendEnter *tmuxio.Call
	for i := range calls {
		if calls[i].Method == "SendText" {
			c := calls[i]
			lastSendText = &c
		}
		if calls[i].Method == "SendEnter" {
			c := calls[i]
			sendEnter = &c
		}
	}
	if lastSendText == nil {
		t.Fatalf("no SendText call: %+v", calls)
	}
	if sendEnter == nil {
		t.Fatalf("no SendEnter call: %+v", calls)
	}
	// The wrapper must contain the sentinel framing AND the user command.
	wrapper := lastSendText.Args[1]
	if !contains(wrapper, "ALFRED_START_nonce-x") {
		t.Fatalf("wrapper missing START sentinel: %q", wrapper)
	}
	if !contains(wrapper, "cmd-1") {
		t.Fatalf("wrapper missing cmdID: %q", wrapper)
	}
	if !contains(wrapper, "echo hi") {
		t.Fatalf("wrapper missing user command: %q", wrapper)
	}
	if !contains(wrapper, "ALFRED_END_nonce-x") {
		t.Fatalf("wrapper missing END sentinel: %q", wrapper)
	}
}

func TestTmuxShell_Write_RejectsConcurrent(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	_ = ts.Start()
	defer ts.Close()

	if err := ts.Write("first", "sleep 10"); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	err := ts.Write("second", "ls")
	if err == nil || err != ErrBusy {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestTmuxShell_Write_RejectsAfterClose(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	_ = ts.Start()
	_ = ts.Close()

	err := ts.Write("any", "ls")
	if err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestTmuxShell_ParserEvent_PublishesEnded(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	_ = ts.Start()
	defer ts.Close()

	sub, cancel := ts.SubscribeEvents(8)
	defer cancel()

	_ = ts.Write("cmd-X", "ls")

	// Simulate the parser delivering START / CHUNK / END events that
	// would normally come from bytes flowing through StreamReader.
	ts.onParserEvent(ParseEvent{Kind: EventStart, CmdID: "cmd-X", Cwd: "/tmp"})
	ts.onParserEvent(ParseEvent{Kind: EventChunk, CmdID: "cmd-X", Bytes: []byte("hello\n")})
	ts.onParserEvent(ParseEvent{Kind: EventEnd, CmdID: "cmd-X", ExitCode: 0})

	sawEnded := false
	sawStarted := false
	for i := 0; i < 3; i++ {
		select {
		case ev := <-sub.C:
			if ev.Started != nil && ev.Started.CmdID == "cmd-X" {
				sawStarted = true
			}
			if ev.Ended != nil && ev.Ended.CmdID == "cmd-X" {
				sawEnded = true
				if ev.Ended.ExitCode != 0 {
					t.Fatalf("exit code = %d, want 0", ev.Ended.ExitCode)
				}
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timed out waiting for event (started=%v ended=%v)", sawStarted, sawEnded)
		}
	}
	if !sawStarted || !sawEnded {
		t.Fatalf("missing events: started=%v ended=%v", sawStarted, sawEnded)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
