package shell

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// MaxBufferBytes is the per-command in-memory output buffer cap.
const MaxBufferBytes = 10 * 1024 * 1024 // 10 MB

// ErrBusy is returned by Write when a command is already running.
var ErrBusy = errors.New("shell busy: a command is currently running")

// ErrUnavailable is returned by Write when bash has died and restart failed.
var ErrUnavailable = errors.New("shell unavailable")

// CommandEvent is the high-level event consumers receive on the events channel.
// One of Started / Chunk / Ended is non-nil per event.
type CommandEvent struct {
	Started *StartedEvent
	Chunk   *ChunkEvent
	Ended   *EndedEvent
}

type StartedEvent struct {
	CmdID     string
	Command   string
	Cwd       string
	StartedAt time.Time
}

type ChunkEvent struct {
	CmdID string
	Bytes []byte
	Drop  bool
}

type EndedEvent struct {
	CmdID      string
	ExitCode   int
	FinishedAt time.Time
	Truncated  bool
	// Output is the complete in-memory buffer of bytes the command produced
	// (capped at MaxBufferBytes). Consumers persist this to disk; the Shell
	// module deliberately doesn't talk to storage.
	Output []byte
}

// RunningCommand is the state of a command currently executing.
type RunningCommand struct {
	ID        string
	Command   string
	Cwd       string
	StartedAt time.Time
	Buffer    []byte // copy of all bytes so far, capped at MaxBufferBytes
	Truncated bool
}

// Shell owns one persistent bash and the parser/broadcaster pipeline.
type Shell struct {
	logger *slog.Logger
	nonce  string

	mu           sync.Mutex
	cmd          *exec.Cmd
	pty          *os.File
	parser       *Parser
	available    bool
	currentCmd   *RunningCommand
	shuttingDown bool // set by Close() to suppress auto-restart after kill

	rawBcast *Broadcaster      // raw PTY chunks; not exposed externally
	evtBcast *EventBroadcaster // typed events for external consumers
}

func NewShell(logger *slog.Logger) *Shell {
	// 16 hex chars (8 bytes random) is plenty unique per process.
	var n [8]byte
	_, _ = rand.Read(n[:])
	return &Shell{
		logger:   logger,
		nonce:    hex.EncodeToString(n[:]),
		rawBcast: NewBroadcaster(),
		evtBcast: NewEventBroadcaster(),
	}
}

// Start launches bash. Must be called exactly once before Write/Subscribe.
func (s *Shell) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return errors.New("shell already started")
	}
	return s.startLocked()
}

func (s *Shell) startLocked() error {
	c := exec.Command("bash", "--noprofile", "--norc")
	c.Env = append(os.Environ(),
		"PS1=",
		"PS2=",
		"PS3=",
		"PS4=",
		"TERM=dumb",
		// Disable readline completion noise.
		"INPUTRC=/dev/null",
	)
	f, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("pty start: %w", err)
	}
	s.cmd = c
	s.pty = f
	s.parser = NewParser(s.nonce)
	s.parser.OnEvent = s.onParserEvent
	s.available = true

	// Turn off PTY echo. Without this, bash would echo every byte of the
	// wrapped command (START printf + user command + END printf) back through
	// the PTY as literal text. Those echoed bytes arrive in the parser AFTER
	// the START sentinel has put it in stateInside, so they'd leak into the
	// command's visible output as garbage like:
	//   __alfred_ec=$?
	//   printf '\x1eALFRED_END_<nonce> <cmdID> %d\x1eX\n' "$__alfred_ec"
	// The echo of "stty -echo" itself harmlessly lands in stateOutside and
	// is dropped by the parser.
	if _, err := f.Write([]byte("stty -echo\n")); err != nil {
		return fmt.Errorf("pty write stty: %w", err)
	}

	// Reader goroutine: feed PTY bytes into parser and raw broadcaster.
	go s.readLoop(f)
	// Wait goroutine: detect bash death.
	go s.waitLoop(c)

	s.logger.Info("shell started", "pid", c.Process.Pid, "nonce", s.nonce)
	return nil
}

func (s *Shell) readLoop(f *os.File) {
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			s.rawBcast.Publish(chunk)
			s.parser.Feed(chunk)
		}
		if err != nil {
			if err != io.EOF {
				s.logger.Info("pty read error", "err", err)
			}
			return
		}
	}
}

func (s *Shell) waitLoop(c *exec.Cmd) {
	err := c.Wait()
	s.mu.Lock()
	s.logger.Error("bash exited", "err", err)

	// Snapshot the Ended event for any in-flight command. We publish AFTER
	// releasing the lock (Publish may take its own locks via subscribers),
	// but we must mark the shell unavailable atomically here — otherwise a
	// concurrent Write() between this Unlock and the restart's re-Lock
	// could see available=true and a dead PTY, queue a command that bash
	// never receives, and permanently brick the shell with an orphan
	// currentCmd that never gets a Started/Ended event.
	var endedEvt *CommandEvent
	if s.currentCmd != nil {
		endedEvt = &CommandEvent{Ended: &EndedEvent{
			CmdID:      s.currentCmd.ID,
			ExitCode:   -1,
			FinishedAt: time.Now().UTC(),
			Truncated:  s.currentCmd.Truncated,
			Output:     s.currentCmd.Buffer,
		}}
	}
	s.currentCmd = nil
	s.available = false
	s.cmd = nil
	s.pty = nil
	shutdown := s.shuttingDown
	s.mu.Unlock()

	if endedEvt != nil {
		s.evtBcast.Publish(*endedEvt)
	}

	// If Close() was called, do not restart bash.
	if shutdown {
		return
	}

	// Otherwise (normal death, or Stop()-initiated death), attempt one restart.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startLocked(); err != nil {
		s.logger.Error("bash restart failed", "err", err)
		s.available = false
		return
	}
	s.logger.Info("bash restarted")
}

// Write submits a user command for execution.
// Returns ErrBusy if another command is running, ErrUnavailable if bash is dead.
func (s *Shell) Write(cmdID, userCmd string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available || s.pty == nil {
		return ErrUnavailable
	}
	if s.currentCmd != nil {
		return ErrBusy
	}
	wrapped := Wrap(s.nonce, cmdID, userCmd)
	// Reserve currentCmd here so a second concurrent Write hits ErrBusy
	// before the START event lands.
	s.currentCmd = &RunningCommand{
		ID:        cmdID,
		Command:   userCmd,
		StartedAt: time.Now().UTC(),
	}
	if _, err := s.pty.Write([]byte(wrapped)); err != nil {
		s.currentCmd = nil
		return fmt.Errorf("pty write: %w", err)
	}
	return nil
}

// Stop kills the currently running command by SIGKILL-ing bash. The waitLoop
// emits an Ended event with a non-zero exit code, then transparently restarts
// bash so the shell is ready for the next command. The trade-off: bash state
// (cwd, env vars, aliases) is reset on Stop. Acceptable because Stop is rare
// and SIGINT-via-PTY routing on macOS proved unreliable (terminal line
// discipline drops queued sentinel printfs).
// No-op if nothing is running.
func (s *Shell) Stop() {
	s.mu.Lock()
	if s.currentCmd == nil || s.cmd == nil || s.cmd.Process == nil {
		s.mu.Unlock()
		return
	}
	proc := s.cmd.Process
	s.mu.Unlock()
	_ = proc.Kill()
}

// Close shuts the shell down for good. After Close, bash will not be
// restarted by the waitLoop. Safe to call from anywhere (e.g. test cleanup)
// regardless of current state. Idempotent.
func (s *Shell) Close() error {
	s.mu.Lock()
	s.shuttingDown = true
	proc := s.cmd
	s.mu.Unlock()
	if proc != nil && proc.Process != nil {
		_ = proc.Process.Kill()
	}
	return nil
}

// CurrentCommand returns a copy of the running command state, or nil if idle.
func (s *Shell) CurrentCommand() *RunningCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentCmd == nil {
		return nil
	}
	cp := *s.currentCmd
	cp.Buffer = append([]byte{}, s.currentCmd.Buffer...)
	return &cp
}

// SubscribeEvents returns a channel of CommandEvents and a cancel func.
func (s *Shell) SubscribeEvents(buffer int) (*EventSubscriber, func()) {
	sub := s.evtBcast.Subscribe(buffer)
	return sub, sub.Close
}

func (s *Shell) onParserEvent(e ParseEvent) {
	s.mu.Lock()
	switch e.Kind {
	case EventStart:
		// The currentCmd was provisionally created in Write; fill in cwd.
		if s.currentCmd != nil && s.currentCmd.ID == e.CmdID {
			s.currentCmd.Cwd = e.Cwd
		} else {
			// Unexpected — perhaps a residual sentinel from a previous lifetime.
			// Synthesize a minimal currentCmd for accounting.
			s.currentCmd = &RunningCommand{
				ID:        e.CmdID,
				Cwd:       e.Cwd,
				StartedAt: time.Now().UTC(),
			}
		}
		evt := CommandEvent{Started: &StartedEvent{
			CmdID:     s.currentCmd.ID,
			Command:   s.currentCmd.Command,
			Cwd:       s.currentCmd.Cwd,
			StartedAt: s.currentCmd.StartedAt,
		}}
		s.mu.Unlock()
		s.evtBcast.Publish(evt)
	case EventChunk:
		if s.currentCmd == nil {
			s.mu.Unlock()
			return
		}
		drop := false
		if len(s.currentCmd.Buffer)+len(e.Bytes) <= MaxBufferBytes {
			s.currentCmd.Buffer = append(s.currentCmd.Buffer, e.Bytes...)
		} else {
			// Buffer full: do not append. Mark truncated.
			if !s.currentCmd.Truncated {
				s.currentCmd.Truncated = true
				drop = true
			}
		}
		evt := CommandEvent{Chunk: &ChunkEvent{
			CmdID: s.currentCmd.ID,
			Bytes: append([]byte{}, e.Bytes...),
			Drop:  drop,
		}}
		s.mu.Unlock()
		s.evtBcast.Publish(evt)
	case EventEnd:
		if s.currentCmd == nil {
			s.mu.Unlock()
			return
		}
		truncated := s.currentCmd.Truncated
		// Capture the buffer before clearing currentCmd; consumers need it
		// to persist output to disk.
		buffer := s.currentCmd.Buffer
		s.currentCmd = nil
		s.mu.Unlock()
		s.evtBcast.Publish(CommandEvent{Ended: &EndedEvent{
			CmdID:      e.CmdID,
			ExitCode:   e.ExitCode,
			FinishedAt: time.Now().UTC(),
			Truncated:  truncated,
			Output:     buffer,
		}})
	}
}
