package shell

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

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

// timeNowUTC is a var so tests can stub if needed; not stubbed in this plan.
var timeNowUTC = func() time.Time { return time.Now().UTC() }

// onParserEvent is the bridge from sentinel parser events to
// CommandEvent broadcaster publishes.
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
