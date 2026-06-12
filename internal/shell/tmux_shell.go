package shell

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
)

// TmuxShell is a Shell-compatible facade over one tmux session.
type TmuxShell struct {
	cfg TmuxShellConfig

	mu                 sync.Mutex
	started            bool
	closed             bool
	currentCmd         *RunningCommand
	parser             *Parser
	reader             *tmuxio.StreamReader
	rawBcast           *Broadcaster
	evtBcast           *EventBroadcaster
	stoppingForRespawn bool

	stopReadLoop chan struct{}
	stopPoller   chan struct{}

	// Test hooks. killPID defaults to syscall.Kill SIGKILL; tests
	// override it to avoid actually signalling foreign PIDs.
	// pollerInterval defaults to 1 second; tests shorten it.
	// OnUserExit is invoked once when the pane goes dead voluntarily
	// (bash `exit` / Ctrl-D) — i.e., not as part of Stop+respawn.
	killPID        func(pid int) error
	pollerInterval time.Duration
	OnUserExit     func()
}

type TmuxShellConfig struct {
	SessionID  string
	Nonce      string
	Runner     tmuxio.TmuxRunner
	StreamPath string
	OffsetPath string
	Logger     *slog.Logger
}

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
		cfg:            cfg,
		rawBcast:       NewBroadcaster(),
		evtBcast:       NewEventBroadcaster(),
		stopReadLoop:   make(chan struct{}),
		stopPoller:     make(chan struct{}),
		killPID:        func(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) },
		pollerInterval: 1 * time.Second,
	}, nil
}

func (ts *TmuxShell) pipeCmd() string {
	return "cat >> " + ts.cfg.StreamPath
}

// Start creates the tmux session, sets remain-on-exit, disables PTY
// echo, starts the pipe, and launches the read-loop + poller
// goroutines. Idempotent: calling Start twice returns an error.
//
// On partial failure (any tmux call after NewSession errors), the
// session may exist in a half-configured state. Callers MUST call
// Close to clean it up — Close is itself idempotent and safe to call
// even when Start returned an error.
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
	if err := r.SendText(ts.cfg.SessionID, "stty -echo"); err != nil {
		return fmt.Errorf("send stty -echo text: %w", err)
	}
	if err := r.SendEnter(ts.cfg.SessionID); err != nil {
		return fmt.Errorf("send stty -echo enter: %w", err)
	}
	if err := r.PipePane(ts.cfg.SessionID, ts.pipeCmd()); err != nil {
		return fmt.Errorf("start pipe-pane: %w", err)
	}

	ts.parser = NewParser(ts.cfg.Nonce)
	ts.parser.OnEvent = ts.onParserEvent

	reader, err := tmuxio.NewStreamReader(ts.cfg.StreamPath, ts.cfg.OffsetPath, parserSink{ts.parser})
	if err != nil {
		return fmt.Errorf("open stream reader: %w", err)
	}
	ts.reader = reader

	go ts.readLoop()
	go ts.poller()

	ts.cfg.Logger.Info("tmux shell started",
		"session", ts.cfg.SessionID,
		"nonce", ts.cfg.Nonce,
	)
	return nil
}

func (ts *TmuxShell) Close() error {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return nil
	}
	ts.closed = true
	ts.mu.Unlock()

	close(ts.stopReadLoop)
	close(ts.stopPoller)

	if r := ts.readerSnap(); r != nil {
		_ = r.Close()
	}
	if err := ts.cfg.Runner.KillSession(ts.cfg.SessionID); err != nil {
		return fmt.Errorf("kill tmux session: %w", err)
	}
	return nil
}

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
		StartedAt: time.Now().UTC(),
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

func (ts *TmuxShell) SubscribeEvents(buffer int) (*EventSubscriber, func()) {
	sub := ts.evtBcast.Subscribe(buffer)
	return sub, sub.Close
}

func (ts *TmuxShell) SubscribeRaw(buffer int) *Subscriber {
	return ts.rawBcast.Subscribe(buffer)
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
	// On the success path we want to clear stoppingForRespawn. On a
	// partial-failure path (kill succeeded, respawn or stty failed),
	// the bash is dead, the session is half-broken, and OnUserExit
	// would be wrong — leave the flag set so the poller stays silent.
	respawned := false
	defer func() {
		if !respawned {
			return
		}
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
	respawned = true
}

func (ts *TmuxShell) onParserEvent(e ParseEvent) {
	ts.mu.Lock()
	switch e.Kind {
	case EventStart:
		if ts.currentCmd != nil && ts.currentCmd.ID == e.CmdID {
			ts.currentCmd.Cwd = e.Cwd
		} else {
			ts.currentCmd = &RunningCommand{
				ID:        e.CmdID,
				Cwd:       e.Cwd,
				StartedAt: time.Now().UTC(),
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
			FinishedAt: time.Now().UTC(),
			Truncated:  truncated,
			Output:     buffer,
		}})
		// Sentinel-aligned truncation (spec §4.4): only safe to fire
		// HERE, between commands. The reader is guaranteed to be in
		// stateOutside (parser just emitted EventEnd) so the StopPipe
		// → truncate → StartPipe sequence cannot strand bytes from a
		// running command.
		ts.maybeTruncate()
	}
}

// maybeTruncate fires StreamReader.TruncateConsumed if pty.stream
// has grown past StreamTruncateThreshold. No-op if the file is
// smaller, or if Resume has not finished wiring the reader yet.
func (ts *TmuxShell) maybeTruncate() {
	r := ts.readerSnap()
	if r == nil {
		return
	}
	info, err := os.Stat(ts.cfg.StreamPath)
	if err != nil || info.Size() < StreamTruncateThreshold {
		return
	}
	if err := r.TruncateConsumed(ts.cfg.Runner, ts.cfg.SessionID, ts.pipeCmd()); err != nil {
		ts.cfg.Logger.Error("truncate pty.stream", "session", ts.cfg.SessionID, "err", err)
	}
}

// StreamTruncateThreshold is the on-disk size of pty.stream above
// which we fire the truncation dance at the next idle boundary.
// Aligned with spec §4.4: 8 MiB. Plan 13's E2E hits this directly
// with two 6-MiB outputs around a small middle command.
const StreamTruncateThreshold = 8 * 1024 * 1024

// parserSink adapts the shell.Parser (concrete type) to the
// tmuxio.ParserSink interface so StreamReader can deliver bytes.
type parserSink struct {
	p *Parser
}

func (s parserSink) Feed(b []byte) {
	s.p.Feed(b)
}

// readerSnap returns the current StreamReader under mu. Nil if Close
// has already released it.
func (ts *TmuxShell) readerSnap() *tmuxio.StreamReader {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.reader
}

func (ts *TmuxShell) readLoop() {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ts.stopReadLoop:
			return
		case <-tick.C:
			r := ts.readerSnap()
			if r == nil {
				continue
			}
			if _, err := r.ReadOnce(); err != nil {
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
