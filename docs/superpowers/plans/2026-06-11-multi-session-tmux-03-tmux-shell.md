# Multi-session Plan 3 — TmuxShell (real tmux backend implementing the Shell event surface)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `TmuxShell` — one instance per tmux session — that exposes the same external surface as the existing `shell.Shell` (`Write`, `Stop`, `Close`, `CurrentCommand`, `SubscribeEvents`) but is backed by `tmuxio.TmuxRunner` + `tmuxio.StreamReader` + the unchanged sentinel `Parser`. Plan 4's `session.Manager` will own N of these.

**Architecture:** `TmuxShell` glues three already-built pieces:
- `tmuxio.TmuxRunner` for talking to the tmux server (the bash + PTY).
- `tmuxio.StreamReader` for reading bytes that pipe-pane appended to `pty.stream`.
- `internal/shell.Parser` (unchanged) for finding sentinel START/END markers in those bytes and emitting events.

The existing `Broadcaster` + `EventBroadcaster` types are reused as-is (they don't depend on PTY ownership). A new `readLoop` goroutine ticks `StreamReader.ReadOnce` on a 50 ms cadence; a separate poller goroutine ticks `PaneDead` once per second for `exit`-auto-close detection.

**Tech Stack:** stdlib + the new `internal/shell/tmuxio` sub-package + the unchanged `internal/shell` parser/broadcasters. No new go.mod entries.

**Spec sections covered:** §3 (component boundary), §4.2 (session creation sequence), §4.3 (command execution path), §4.4 (truncation policy hook), §4.5 (Stop via SIGKILL pane_pid + respawn), §4.6 (exit auto-detection via PaneDead poller).

---

## File Structure

```
internal/shell/
├── shell.go                  # UNCHANGED in this plan; kept for legacy single-bash callers until Plan 7 removes it
├── broadcaster.go            # UNCHANGED — reused as-is
├── sentinel.go               # UNCHANGED — Parser is reused, Wrap() is reused
├── tmux_shell.go             # NEW: TmuxShell type, implements the same surface as Shell
├── tmux_shell_test.go        # NEW: unit tests using FakeRunner
└── tmux_shell_integration_test.go  # NEW: with -tags=integration; real tmux + bash
```

Why keep `shell.go` (the old PTY-direct version) for now: Plan 4 (Manager) and Plan 7 (Boot) will switch all callers to `TmuxShell`. The old type stays as dead code until then — deleting it now would break the existing `internal/api` build before its rewrite lands.

---

## Task 1: TmuxShell skeleton + lifecycle (Start, Close)

The minimal type with a constructor and shutdown, no Write yet. Just enough to make `tmuxio` plumbing observable: we can confirm `New + Close` does the right runner calls.

**Files:**
- Create: `internal/shell/tmux_shell.go`
- Create: `internal/shell/tmux_shell_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/shell/tmux_shell_test.go`:

```go
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
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/shell/ -run TestTmuxShell -count=1`
Expected: BUILD FAILS on `NewTmuxShell`, `TmuxShell`, `TmuxShellConfig` undefined.

- [ ] **Step 3: Implement skeleton (Start, Close only — no Write yet)**

Create `internal/shell/tmux_shell.go`:

```go
package shell

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
)

// TmuxShell is a Shell-compatible facade over one tmux session. It
// reuses the existing Broadcaster / EventBroadcaster / Parser from
// the shell package and adds tmux-specific lifecycle handling.
//
// One TmuxShell == one tmux session == one bash. The Manager (Plan 4)
// owns N of these and maps sessionIDs to instances.
type TmuxShell struct {
	cfg TmuxShellConfig

	mu           sync.Mutex
	started      bool
	closed       bool
	currentCmd   *RunningCommand
	parser       *Parser
	reader       *tmuxio.StreamReader
	rawBcast     *Broadcaster
	evtBcast     *EventBroadcaster
	stoppingForRespawn bool

	stopReadLoop chan struct{}
	stopPoller   chan struct{}
}

// TmuxShellConfig is the immutable dependencies a TmuxShell needs.
// Construction never starts goroutines; call Start() for that.
type TmuxShellConfig struct {
	// SessionID is the tmux session name AND the sessionID used by
	// upstream code (store, API). Caller is responsible for uniqueness.
	SessionID string

	// Nonce is the random hex string sentinel printfs embed. One nonce
	// per Go process; the Parser uses it to ignore stale sentinels
	// that may already be in pty.stream from a prior process.
	Nonce string

	// Runner is the TmuxRunner the shell talks to. FakeRunner in tests,
	// ExecRunner in production.
	Runner tmuxio.TmuxRunner

	// StreamPath is the regular file tmux pipe-pane appends to.
	// OffsetPath is the byte-offset checkpoint. Both files are managed
	// by the StreamReader internally.
	StreamPath string
	OffsetPath string

	Logger *slog.Logger
}

// NewTmuxShell validates the config but does NOT contact tmux. Call
// Start to actually create the session.
func NewTmuxShell(cfg TmuxShellConfig) (*TmuxShell, error) {
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("TmuxShellConfig.SessionID required")
	}
	if cfg.Nonce == "" {
		return nil, fmt.Errorf("TmuxShellConfig.Nonce required")
	}
	if cfg.Runner == nil {
		return nil, fmt.Errorf("TmuxShellConfig.Runner required")
	}
	if cfg.StreamPath == "" || cfg.OffsetPath == "" {
		return nil, fmt.Errorf("TmuxShellConfig.StreamPath and OffsetPath required")
	}
	if cfg.Logger == nil {
		return nil, fmt.Errorf("TmuxShellConfig.Logger required")
	}
	return &TmuxShell{
		cfg:          cfg,
		rawBcast:     NewBroadcaster(),
		evtBcast:     NewEventBroadcaster(),
		stopReadLoop: make(chan struct{}),
		stopPoller:   make(chan struct{}),
	}, nil
}

// pipeCmd returns the shell command pipe-pane runs to forward bytes
// into our StreamPath. Kept as a method so tests can stub via
// the StreamPath field.
func (ts *TmuxShell) pipeCmd() string {
	// Always append; never truncate via this path. Truncation is the
	// StreamReader's responsibility (spec §4.4).
	return "cat >> " + ts.cfg.StreamPath
}

// Start creates the tmux session, sets remain-on-exit, disables PTY
// echo, starts the pipe, and (in Plan 3 Task 3) launches the
// read-loop + poller goroutines. Idempotent: calling Start twice
// returns an error on the second call.
func (ts *TmuxShell) Start() error {
	ts.mu.Lock()
	if ts.started {
		ts.mu.Unlock()
		return fmt.Errorf("TmuxShell %s already started", ts.cfg.SessionID)
	}
	ts.started = true
	ts.mu.Unlock()

	r := ts.cfg.Runner

	if err := r.NewSession(ts.cfg.SessionID, "bash", "--noprofile", "--norc"); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}
	if err := r.SetOption(ts.cfg.SessionID, "remain-on-exit", "on"); err != nil {
		return fmt.Errorf("set remain-on-exit: %w", err)
	}
	// Disable PTY echo (CONTEXT.md "traps": echo lets the wrapper bytes
	// leak into the visible output).
	if err := r.SendText(ts.cfg.SessionID, "stty -echo"); err != nil {
		return fmt.Errorf("send stty -echo text: %w", err)
	}
	if err := r.SendEnter(ts.cfg.SessionID); err != nil {
		return fmt.Errorf("send stty -echo enter: %w", err)
	}
	if err := r.PipePane(ts.cfg.SessionID, ts.pipeCmd()); err != nil {
		return fmt.Errorf("start pipe-pane: %w", err)
	}

	// Wire parser → broadcaster. (StreamReader hookup lands in Task 3.)
	ts.parser = NewParser(ts.cfg.Nonce)
	ts.parser.OnEvent = ts.onParserEvent

	ts.cfg.Logger.Info("tmux shell started",
		"session", ts.cfg.SessionID,
		"nonce", ts.cfg.Nonce,
	)
	return nil
}

// Close kills the tmux session and stops background goroutines.
// Safe to call multiple times.
func (ts *TmuxShell) Close() error {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return nil
	}
	ts.closed = true
	ts.mu.Unlock()

	// Signal goroutines (no-op until Task 3 starts them).
	close(ts.stopReadLoop)
	close(ts.stopPoller)

	if ts.reader != nil {
		_ = ts.reader.Close()
	}
	if err := ts.cfg.Runner.KillSession(ts.cfg.SessionID); err != nil {
		return fmt.Errorf("kill tmux session: %w", err)
	}
	return nil
}

// onParserEvent is the bridge from sentinel parser events to
// CommandEvent broadcaster publishes. Implementation lands with
// the Write path in Task 2.
func (ts *TmuxShell) onParserEvent(e ParseEvent) {
	// Implemented in Task 2.
}
```

- [ ] **Step 4: Run tests, confirm green**

Run: `go test ./internal/shell/ -run TestTmuxShell -count=1 -v`
Expected: 2 PASS (`Start_CreatesSessionAndConfigures`, `Close_KillsSessionAndIsIdempotent`).

- [ ] **Step 5: Commit**

```bash
git add internal/shell/tmux_shell.go internal/shell/tmux_shell_test.go
git commit -m "shell: TmuxShell skeleton — Start creates session+pipe, Close kills + idempotent"
```

---

## Task 2: Wire Write + parser event handling

Send wrapper text + Enter; route parser events into the existing `EventBroadcaster`. Mirrors `internal/shell/shell.go`'s `Write` + `onParserEvent` logic but routes through the runner instead of `pty.Write`.

**Files:**
- Modify: `internal/shell/tmux_shell.go`
- Modify: `internal/shell/tmux_shell_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/shell/tmux_shell_test.go`:

```go
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
```

Add the imports the new tests need (at the top of the file):

```go
import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
)
```

- [ ] **Step 2: Run, confirm failures**

Run: `go test ./internal/shell/ -run TestTmuxShell -count=1`
Expected: BUILD FAILS on `ts.Write`, `ts.SubscribeEvents` undefined.

- [ ] **Step 3: Implement Write, SubscribeEvents, CurrentCommand, onParserEvent**

Append to `internal/shell/tmux_shell.go` (keeping the file under one
package, all in the same file is fine for ~250 lines):

```go
// Write submits a user command for execution.
// Returns ErrBusy if another command is already running,
// ErrUnavailable if the shell is closed.
func (ts *TmuxShell) Write(cmdID, userCmd string) error {
	ts.mu.Lock()
	if ts.closed || !ts.started {
		ts.mu.Unlock()
		return ErrUnavailable
	}
	if ts.currentCmd != nil {
		ts.mu.Unlock()
		return ErrBusy
	}
	ts.currentCmd = &RunningCommand{
		ID:        cmdID,
		Command:   userCmd,
		StartedAt: timeNowUTC(),
	}
	ts.mu.Unlock()

	wrapped := Wrap(ts.cfg.Nonce, cmdID, userCmd)
	if err := ts.cfg.Runner.SendText(ts.cfg.SessionID, wrapped); err != nil {
		ts.mu.Lock()
		ts.currentCmd = nil
		ts.mu.Unlock()
		return fmt.Errorf("send wrapper: %w", err)
	}
	if err := ts.cfg.Runner.SendEnter(ts.cfg.SessionID); err != nil {
		ts.mu.Lock()
		ts.currentCmd = nil
		ts.mu.Unlock()
		return fmt.Errorf("send enter: %w", err)
	}
	return nil
}

// CurrentCommand returns a copy of the running command state, or nil if idle.
func (ts *TmuxShell) CurrentCommand() *RunningCommand {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.currentCmd == nil {
		return nil
	}
	cp := *ts.currentCmd
	cp.Buffer = append([]byte{}, ts.currentCmd.Buffer...)
	return &cp
}

// SubscribeEvents returns a typed-event subscriber plus a cancel func.
// Mirrors shell.Shell.SubscribeEvents so callers can swap one type for
// the other without changing event-consumption code.
func (ts *TmuxShell) SubscribeEvents(buffer int) (*EventSubscriber, func()) {
	sub := ts.evtBcast.Subscribe(buffer)
	return sub, sub.Close
}

// SubscribeRaw returns the raw byte stream subscriber. Used by the
// disk-writer in api/ws.go (a future plan; defined here for parity).
func (ts *TmuxShell) SubscribeRaw(buffer int) *Subscriber {
	return ts.rawBcast.Subscribe(buffer)
}

func (ts *TmuxShell) onParserEvent(e ParseEvent) {
	ts.mu.Lock()
	switch e.Kind {
	case EventStart:
		// Fill in cwd, publish Started.
		if ts.currentCmd != nil && ts.currentCmd.ID == e.CmdID {
			ts.currentCmd.Cwd = e.Cwd
		} else {
			// Sentinel for a command we don't have a record of —
			// synthesize one (e.g., parsing a residual sentinel from
			// before a Go restart while currentCmd was already cleared).
			ts.currentCmd = &RunningCommand{
				ID:        e.CmdID,
				Cwd:       e.Cwd,
				StartedAt: timeNowUTC(),
			}
		}
		evt := CommandEvent{Started: &StartedEvent{
			CmdID:     ts.currentCmd.ID,
			Command:   ts.currentCmd.Command,
			Cwd:       ts.currentCmd.Cwd,
			StartedAt: ts.currentCmd.StartedAt,
		}}
		ts.mu.Unlock()
		ts.evtBcast.Publish(evt)
	case EventChunk:
		if ts.currentCmd == nil {
			ts.mu.Unlock()
			return
		}
		drop := false
		if len(ts.currentCmd.Buffer)+len(e.Bytes) <= MaxBufferBytes {
			ts.currentCmd.Buffer = append(ts.currentCmd.Buffer, e.Bytes...)
		} else if !ts.currentCmd.Truncated {
			ts.currentCmd.Truncated = true
			drop = true
		}
		evt := CommandEvent{Chunk: &ChunkEvent{
			CmdID: ts.currentCmd.ID,
			Bytes: append([]byte{}, e.Bytes...),
			Drop:  drop,
		}}
		ts.mu.Unlock()
		ts.evtBcast.Publish(evt)
	case EventEnd:
		if ts.currentCmd == nil {
			ts.mu.Unlock()
			return
		}
		truncated := ts.currentCmd.Truncated
		buffer := ts.currentCmd.Buffer
		ts.currentCmd = nil
		ts.mu.Unlock()
		ts.evtBcast.Publish(CommandEvent{Ended: &EndedEvent{
			CmdID:      e.CmdID,
			ExitCode:   e.ExitCode,
			FinishedAt: timeNowUTC(),
			Truncated:  truncated,
			Output:     buffer,
		}})
	}
}

// timeNowUTC is a var so tests can stub if needed; not stubbed in this plan.
var timeNowUTC = func() time.Time { return time.Now().UTC() }
```

You'll need to add `"time"` to the import block at the top of `tmux_shell.go`.

- [ ] **Step 4: Run tests, confirm green**

Run: `go test ./internal/shell/ -run TestTmuxShell -count=1 -v`
Expected: 5 PASS (the 2 from Task 1 + `Write_SendsWrapperThenEnter`, `Write_RejectsConcurrent`, `Write_RejectsAfterClose`, `ParserEvent_PublishesEnded` — actually 6 total).

- [ ] **Step 5: Commit**

```bash
git add internal/shell/tmux_shell.go internal/shell/tmux_shell_test.go
git commit -m "shell: TmuxShell Write + parser-event bridging via existing broadcasters"
```

---

## Task 3: Read-loop goroutine + Stop + PaneDead poller

The piece that pulls bytes from the file and feeds the parser, plus
the Stop logic (SIGKILL pane_pid → respawn-pane → re-send `stty
-echo`), plus the once-a-second `pane_dead` poller that auto-closes
when the user types `exit`.

**Files:**
- Modify: `internal/shell/tmux_shell.go`
- Modify: `internal/shell/tmux_shell_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/shell/tmux_shell_test.go`:

```go
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

func TestTmuxShell_PaneDeadPoller_SuppressedDuringRespawn(t *testing.T) {
	fr := tmuxio.NewFakeRunner()
	ts, _ := newTestTmuxShell(t, fr)
	ts.OnUserExit = func() {
		t.Fatal("OnUserExit fired during Stop+respawn — should be suppressed")
	}
	ts.pollerInterval = 20 * time.Millisecond
	ts.killPID = func(pid int) error { return nil }

	_ = ts.Start()
	defer ts.Close()

	_ = ts.Write("cmd-R", "sleep 60")

	// Simulate: Stop sets stoppingForRespawn → kills bash → pane goes dead
	// BUT before the poller observes it, RespawnPane resets it. To race
	// the poller deterministically we manually flip the dead state.
	fr.MarkPaneDead("sess-1")
	ts.Stop()
	// Give the poller a few ticks to *not* fire.
	time.Sleep(100 * time.Millisecond)
}
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/shell/ -run TestTmuxShell -count=1`
Expected: BUILD FAILS on `ts.killPID`, `ts.Stop`, `ts.OnUserExit`, `ts.pollerInterval` undefined.

- [ ] **Step 3: Implement readLoop, Stop, poller**

Append to `internal/shell/tmux_shell.go` (and add `"syscall"` + `"os"` to imports if not already there):

```go
// killPID is overridable for tests; production sends SIGKILL.
var defaultKillPID = func(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// Expose hooks for tests to override (declared as fields, not vars,
// so each TmuxShell instance is independent).
//
// killPID: how Stop terminates the bash process. Defaults to SIGKILL.
// pollerInterval: how often the poller checks pane_dead. Defaults 1s.
// OnUserExit: called once per voluntary bash exit (Ctrl-D / `exit`).
type tmuxShellHooks struct {
	killPID        func(pid int) error
	pollerInterval time.Duration
	OnUserExit     func()
}

func init() {
	// Just to satisfy the linter — see field declarations below.
	_ = defaultKillPID
}

// Start kicks off the readLoop and PaneDead poller goroutines AFTER
// the session is set up. Append to the existing Start() body
// after the parser is wired:
```

That `init()` chatter is misleading — the real change is to add fields
to `TmuxShell` and extend `Start()`. Here is the **full set of
modifications** for `tmux_shell.go` in one block (apply in place):

```go
// At the package level (replace any earlier sketch), define:
import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
)

// In the TmuxShell struct, add these fields (alongside the ones from Task 1):
//   killPID          func(pid int) error
//   pollerInterval   time.Duration
//   OnUserExit       func()
//
// And in NewTmuxShell, default them:
//   ts.killPID = func(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
//   ts.pollerInterval = 1 * time.Second

// readLoop runs until stopReadLoop closes. Polls StreamReader at a
// fast cadence (~50ms) — small enough that streaming output feels live,
// large enough that an idle session uses ~0% CPU.
func (ts *TmuxShell) readLoop() {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ts.stopReadLoop:
			return
		case <-tick.C:
			if ts.reader == nil {
				continue
			}
			if _, err := ts.reader.ReadOnce(); err != nil {
				ts.cfg.Logger.Error("stream read error", "session", ts.cfg.SessionID, "err", err)
			}
		}
	}
}

// poller checks #{pane_dead} every pollerInterval. If the pane is
// dead AND we're not in a Stop-respawn cycle, the user voluntarily
// exited (Ctrl-D or `exit`) → fire OnUserExit exactly once.
func (ts *TmuxShell) poller() {
	tick := time.NewTicker(ts.pollerInterval)
	defer tick.Stop()
	fired := false
	for {
		select {
		case <-ts.stopPoller:
			return
		case <-tick.C:
			dead, err := ts.cfg.Runner.PaneDead(ts.cfg.SessionID)
			if err != nil {
				continue
			}
			if !dead {
				continue
			}
			ts.mu.Lock()
			stopping := ts.stoppingForRespawn
			ts.mu.Unlock()
			if stopping {
				continue
			}
			if fired {
				continue
			}
			fired = true
			if ts.OnUserExit != nil {
				ts.OnUserExit()
			}
		}
	}
}

// Stop runs the safe sequence from spec §4.5:
//   1. mark stoppingForRespawn (suppresses the poller)
//   2. fetch the pane PID and SIGKILL it
//   3. respawn-pane with a fresh bash
//   4. resend stty -echo
//   5. unmark stoppingForRespawn
func (ts *TmuxShell) Stop() {
	ts.mu.Lock()
	if ts.currentCmd == nil {
		ts.mu.Unlock()
		return
	}
	ts.stoppingForRespawn = true
	ts.mu.Unlock()
	defer func() {
		ts.mu.Lock()
		ts.stoppingForRespawn = false
		ts.mu.Unlock()
	}()

	pid, err := ts.cfg.Runner.PanePID(ts.cfg.SessionID)
	if err != nil {
		ts.cfg.Logger.Error("Stop: PanePID failed", "err", err)
		return
	}
	if pid > 0 {
		if err := ts.killPID(pid); err != nil {
			ts.cfg.Logger.Error("Stop: kill bash failed", "pid", pid, "err", err)
			return
		}
	}
	if err := ts.cfg.Runner.RespawnPane(ts.cfg.SessionID, "bash", "--noprofile", "--norc"); err != nil {
		ts.cfg.Logger.Error("Stop: RespawnPane failed", "err", err)
		return
	}
	if err := ts.cfg.Runner.SendText(ts.cfg.SessionID, "stty -echo"); err != nil {
		ts.cfg.Logger.Error("Stop: SendText stty -echo failed", "err", err)
		return
	}
	if err := ts.cfg.Runner.SendEnter(ts.cfg.SessionID); err != nil {
		ts.cfg.Logger.Error("Stop: SendEnter failed", "err", err)
		return
	}
}
```

Inside the existing `Start()` body, AFTER the parser is wired and
BEFORE the `Info` log, attach the reader and launch the two goroutines:

```go
	reader, err := tmuxio.NewStreamReader(ts.cfg.StreamPath, ts.cfg.OffsetPath, parserSink{ts.parser})
	if err != nil {
		return fmt.Errorf("open stream reader: %w", err)
	}
	ts.reader = reader

	go ts.readLoop()
	go ts.poller()
```

And add the tiny adaptor (somewhere near `onParserEvent`):

```go
// parserSink adapts the existing shell.Parser (Feed method on a
// pointer receiver) to tmuxio.ParserSink (interface with one Feed
// method). Necessary because tmuxio cannot import shell (would
// cycle); shell imports tmuxio, not the other way.
type parserSink struct {
	p *Parser
}

func (s parserSink) Feed(b []byte) {
	s.p.Feed(b)
}
```

And in `NewTmuxShell`, set the default hook values:

```go
	ts.killPID = func(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
	ts.pollerInterval = 1 * time.Second
```

(Place these two lines just before `return ts, nil`.)

- [ ] **Step 4: Run tests, confirm green**

Run: `go test ./internal/shell/ -race -run TestTmuxShell -count=1 -v`
Expected: 10 PASS (6 from previous tasks + 4 new: `ReadLoop_DeliversBytesToParser`, `Stop_KillsPanePIDAndRespawns`, `PaneDeadPoller_FiresExitCallback`, `PaneDeadPoller_SuppressedDuringRespawn`).

- [ ] **Step 5: Commit**

```bash
git add internal/shell/tmux_shell.go internal/shell/tmux_shell_test.go
git commit -m "shell: TmuxShell readLoop + Stop (kill+respawn) + pane-dead poller for exit auto-close"
```

---

## Task 4: Integration test with real tmux

Behind a build tag so CI without tmux still passes. Drives the full
loop: NewTmuxShell → Start → Write a real command → wait for Ended →
verify output.

**Files:**
- Create: `internal/shell/tmux_shell_integration_test.go`

- [ ] **Step 1: Write the integration test**

Create `internal/shell/tmux_shell_integration_test.go`:

```go
//go:build integration
// +build integration

package shell

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
)

func TestIntegration_TmuxShell_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "tmux.sock")
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
				if !contains(string(ev.Ended.Output), "INTEG_OK") {
					t.Fatalf("output missing INTEG_OK: %q", ev.Ended.Output)
				}
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("timeout waiting for Ended event from real tmux")
}
```

- [ ] **Step 2: Run with the integration build tag (skips if no tmux)**

Run: `go test -tags=integration -race ./internal/shell/ -run TestIntegration -count=1 -v`
Expected on a host with tmux: PASS.
Expected on a host without tmux: SKIP.

If it FAILS, inspect `/tmp/pty.stream*` files from the failing test run (the `t.TempDir()` is cleaned up but if you remove the cleanup you can pry inside).

- [ ] **Step 3: Commit**

```bash
git add internal/shell/tmux_shell_integration_test.go
git commit -m "shell: integration test driving TmuxShell against real tmux + bash"
```

---

## Plan 3 acceptance

- `go test -race ./internal/shell/ -run TestTmuxShell -count=1` is green (10 unit tests).
- `go test -tags=integration -race ./internal/shell/ -run TestIntegration -count=1` is green where tmux is available, SKIP elsewhere.
- The old `shell.Shell` type continues to exist and compile (we did NOT touch its file); legacy callers in `internal/api` still build against it. Plan 4-5 swap them.
- `TmuxShell` exposes `Write`, `Stop`, `Close`, `CurrentCommand`, `SubscribeEvents`, `SubscribeRaw` — the exact subset of the `Shell` surface that `internal/api` uses today.
- `OnUserExit` callback is wired and gated against the Stop-respawn cycle.

---

## Plan 3 self-review checklist

- [ ] `grep -rE "TODO|FIXME|XXX" internal/shell/tmux_shell*` is empty.
- [ ] `go vet ./internal/shell/` is clean.
- [ ] `go test -race -count=1 ./internal/shell/` shows the 4 commits' worth of new tests all green.
- [ ] Imports list in `tmux_shell.go` is minimal — only stdlib + `internal/shell/tmuxio` (no circular import).
- [ ] `parserSink` adaptor is used (otherwise the `tmuxio.ParserSink` interface is unsatisfied at the `NewStreamReader` call).
- [ ] `git log --oneline | head -4` shows the four commits from this plan.
