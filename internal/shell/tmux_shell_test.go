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

func TestTmuxShell_ReadLoop_DeliversBytesToParser(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, dir := newTestTmuxShell(t, fr)
	_ = ts.Start()
	defer ts.Close()

	sub, cancel := ts.SubscribeEvents(8)
	defer cancel()

	_ = ts.Write("cmd-Z", "ls")
	// Write the bytes that bash would have produced through pipe-pane.
	wrapper := Wrap("nonce-x", "cmd-Z", "ls")
	_ = wrapper // for readability; we synthesise the response directly
	streamFile := filepath.Join(dir, "pty.stream")
	body := "\x1eALFRED_START_nonce-x cmd-Z /tmp\x1eX\nhello\n\x1eALFRED_END_nonce-x cmd-Z 0\x1eX\n"
	f, _ := os.OpenFile(streamFile, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write([]byte(body))
	_ = f.Close()

	sawEnded := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sawEnded {
		select {
		case ev := <-sub.C:
			if ev.Ended != nil && ev.Ended.CmdID == "cmd-Z" {
				sawEnded = true
				if ev.Ended.ExitCode != 0 {
					t.Fatalf("exit code = %d", ev.Ended.ExitCode)
				}
				if string(ev.Ended.Output) != "hello\n" {
					t.Fatalf("output = %q", ev.Ended.Output)
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !sawEnded {
		t.Fatalf("read loop never produced Ended event")
	}
}

func TestTmuxShell_Stop_KillsPanePIDAndRespawns(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	_ = ts.Start()
	defer ts.Close()

	_ = ts.Write("cmd-S", "sleep 60")
	// Replace ts.killPID so tests don't actually syscall.Kill anything.
	killed := 0
	ts.killPID = func(pid int) error {
		killed++
		return nil
	}
	ts.Stop()
	// Stop should have:
	//   1. PanePID(sess-1)
	//   2. killPID(<pid>)
	//   3. RespawnPane(sess-1, bash, --noprofile, --norc)
	//   4. SendText stty -echo + SendEnter
	calls := fr.Calls()
	sawPanePID, sawRespawn := false, false
	for _, c := range calls {
		if c.Method == "PanePID" {
			sawPanePID = true
		}
		if c.Method == "RespawnPane" {
			sawRespawn = true
		}
	}
	if !sawPanePID {
		t.Fatalf("Stop did not call PanePID: %+v", calls)
	}
	if killed != 1 {
		t.Fatalf("Stop should call killPID once, got %d", killed)
	}
	if !sawRespawn {
		t.Fatalf("Stop did not RespawnPane: %+v", calls)
	}
}

func TestTmuxShell_PaneDeadPoller_FiresExitCallback(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)

	called := make(chan struct{}, 1)
	ts.OnUserExit = func() {
		select {
		case called <- struct{}{}:
		default:
		}
	}
	// Make the poller tick very fast for the test.
	ts.pollerInterval = 20 * time.Millisecond

	_ = ts.Start()
	defer ts.Close()

	// Mark pane dead; the poller should observe and fire the callback.
	fr.MarkPaneDead("sess-1")

	select {
	case <-called:
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnUserExit was not called within 500ms after pane death")
	}
}

func TestTmuxShell_Stop_ClearsCurrentCmdAndAllowsNextWrite(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	_ = ts.Start()
	defer ts.Close()
	ts.killPID = func(int) error { return nil }

	sub, cancel := ts.SubscribeEvents(8)
	defer cancel()

	_ = ts.Write("cmd-stop", "sleep 60")
	ts.Stop()

	// Stop must publish an Ended event with negative ExitCode so the
	// upstream WS/store layers know the command was killed, not finished.
	sawStopEnded := false
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && !sawStopEnded {
		select {
		case ev := <-sub.C:
			if ev.Ended != nil && ev.Ended.CmdID == "cmd-stop" {
				sawStopEnded = true
				if ev.Ended.ExitCode != -1 {
					t.Fatalf("exit code = %d, want -1", ev.Ended.ExitCode)
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !sawStopEnded {
		t.Fatal("Stop did not publish Ended event")
	}

	// After Stop the shell must accept a new Write — currentCmd cleared.
	if cur := ts.CurrentCommand(); cur != nil {
		t.Fatalf("CurrentCommand still set after Stop: %+v", cur)
	}
	if err := ts.Write("post-stop", "ls"); err != nil {
		t.Fatalf("Write after Stop should succeed, got %v", err)
	}
}

func TestTmuxShell_PaneDeadPoller_SuppressedDuringRespawn(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	ts.OnUserExit = func() {
		t.Fatal("OnUserExit fired while stoppingForRespawn=true — should be suppressed")
	}
	ts.pollerInterval = 20 * time.Millisecond

	_ = ts.Start()
	defer ts.Close()

	// Force the suppression flag on directly. (Stop also sets it, but
	// Stop runs synchronously and we want to assert the poller observes
	// the flag and skips. Setting it here makes the test deterministic.)
	ts.mu.Lock()
	ts.stoppingForRespawn = true
	ts.mu.Unlock()
	fr.MarkPaneDead("sess-1")

	// Give the poller at least 5 ticks. None should fire OnUserExit.
	time.Sleep(150 * time.Millisecond)
}
