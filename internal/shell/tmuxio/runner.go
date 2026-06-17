// Package tmuxio is the I/O boundary between alfred-server and the
// external tmux server. Every tmux subcommand the application issues
// goes through TmuxRunner; tests substitute FakeRunner for hermetic
// unit tests.
//
// This package owns no goroutines and no long-lived state. It is a
// pure transformer from "what alfred wants" to "exec(tmux)".
package tmuxio

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TmuxRunner is the minimal set of tmux subcommands alfred-server uses.
// Implementations: ExecRunner (production), FakeRunner (tests).
type TmuxRunner interface {
	// ListSessions returns the names of all sessions known to the tmux
	// server. Returns ([], nil) if the server is not running.
	ListSessions() ([]string, error)

	// NewSession creates a detached session named name running command
	// with args inside it. Equivalent to `tmux new-session -d -s <name> <command> <args...>`.
	NewSession(name, command string, args ...string) error

	// KillSession terminates the session named name. Idempotent: returns
	// nil if the session does not exist.
	KillSession(name string) error

	// SendText writes the bytes in text into the session's pane
	// LITERALLY. Special characters like ', ", \, $, and the key names
	// Enter, Up, etc. are passed through as-is. The shell sees them as
	// characters typed by a user. It does NOT execute the buffered line
	// until Enter is sent separately.
	SendText(session, text string) error

	// SendEnter delivers a single Enter keypress. tmux interprets the
	// literal token "Enter" as the key (NOT the four characters
	// E-n-t-e-r), which makes bash execute the line that was previously
	// typed via SendText.
	SendEnter(session string) error

	// PanePID returns the PID of the program currently running in the
	// session's (only) pane.
	PanePID(session string) (int, error)

	// PaneDead reports whether the pane's program has exited (set when
	// remain-on-exit is on).
	PaneDead(session string) (bool, error)

	// PaneCurrentPath returns the cwd of the pane's foreground process
	// (typically bash). tmux maintains this by polling each pane's
	// shell-tracked working directory; it's authoritative and zero-
	// state for us. Used to decide which dir `claude -p` should be
	// invoked in so users get "ls / Read / Write happen where I cd'd to".
	PaneCurrentPath(session string) (string, error)

	// SetOption applies `tmux set-option -t <session> <name> <value>`.
	SetOption(session, name, value string) error

	// PipePane starts piping the pane's output to the given shell command.
	// Passing an empty cmd stops the active pipe. When starting a pipe, the
	// -o flag is included to replace any previously active pipe rather than
	// no-op when one already exists.
	PipePane(session, cmd string) error

	// RespawnPane kills whatever is running in the pane (if any) and
	// starts a fresh program. Equivalent to `tmux respawn-pane -k -t <session> <command> <args...>`.
	RespawnPane(session, command string, args ...string) error
}

// ExecRunner is the production TmuxRunner. It shells out to the tmux
// binary on a configurable socket path.
type ExecRunner struct {
	socket string
}

// NewExecRunner returns a TmuxRunner bound to the given UNIX socket
// path. Pass the absolute path of the socket file; tmux's default
// path is not used.
func NewExecRunner(socket string) *ExecRunner {
	return &ExecRunner{socket: socket}
}

func (e *ExecRunner) cmd(args ...string) *exec.Cmd {
	all := append([]string{"-S", e.socket}, args...)
	return exec.Command("tmux", all...)
}

func (e *ExecRunner) ListSessions() ([]string, error) {
	out, err := e.cmd("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		// Treat both "no server running on <sock>" and "error connecting
		// to <sock>" (the latter happens when the socket file is absent
		// on macOS) as "0 sessions, not an error".
		stderr := exitStderr(err)
		if strings.Contains(stderr, "no server running") ||
			strings.Contains(stderr, "error connecting") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w (stderr=%q)", err, stderr)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		names = append(names, line)
	}
	return names, nil
}

func (e *ExecRunner) NewSession(name, command string, args ...string) error {
	full := append([]string{"new-session", "-d", "-s", name, command}, args...)
	out, err := e.cmd(full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux new-session %s: %w (out=%q)", name, err, out)
	}
	return nil
}

func (e *ExecRunner) KillSession(name string) error {
	out, err := e.cmd("kill-session", "-t", name).CombinedOutput()
	if err != nil {
		// "session not found" => already dead, idempotent success.
		if bytes.Contains(out, []byte("can't find session")) {
			return nil
		}
		// "no server running" => also idempotent.
		if bytes.Contains(out, []byte("no server running")) {
			return nil
		}
		return fmt.Errorf("tmux kill-session %s: %w (out=%q)", name, err, out)
	}
	return nil
}

func (e *ExecRunner) SendText(session, text string) error {
	// The -l flag sends bytes literally (no key-name interpretation).
	out, err := e.cmd("send-keys", "-t", session, "-l", text).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys -l %s: %w (out=%q)", session, err, out)
	}
	return nil
}

func (e *ExecRunner) SendEnter(session string) error {
	// Without -l, tmux interprets "Enter" as the named key.
	out, err := e.cmd("send-keys", "-t", session, "Enter").CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys Enter %s: %w (out=%q)", session, err, out)
	}
	return nil
}

func (e *ExecRunner) PanePID(session string) (int, error) {
	out, err := e.cmd("display-message", "-t", session, "-p", "#{pane_pid}").Output()
	if err != nil {
		return 0, fmt.Errorf("tmux display-message %s pane_pid: %w (stderr=%q)", session, err, exitStderr(err))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse pane_pid %q: %w", out, err)
	}
	return pid, nil
}

func (e *ExecRunner) PaneCurrentPath(session string) (string, error) {
	out, err := e.cmd("display-message", "-t", session, "-p", "#{pane_current_path}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux display-message %s pane_current_path: %w (stderr=%q)", session, err, exitStderr(err))
	}
	return strings.TrimSpace(string(out)), nil
}

func (e *ExecRunner) PaneDead(session string) (bool, error) {
	out, err := e.cmd("display-message", "-t", session, "-p", "#{pane_dead}").Output()
	if err != nil {
		return false, fmt.Errorf("tmux display-message %s pane_dead: %w (stderr=%q)", session, err, exitStderr(err))
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

func (e *ExecRunner) SetOption(session, name, value string) error {
	out, err := e.cmd("set-option", "-t", session, name, value).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux set-option %s %s=%s: %w (out=%q)", session, name, value, err, out)
	}
	return nil
}

func (e *ExecRunner) PipePane(session, cmd string) error {
	args := []string{"pipe-pane", "-t", session}
	if cmd != "" {
		args = append(args, "-o", cmd)
	}
	out, err := e.cmd(args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux pipe-pane %s: %w (out=%q)", session, err, out)
	}
	return nil
}

func (e *ExecRunner) RespawnPane(session, command string, args ...string) error {
	full := append([]string{"respawn-pane", "-k", "-t", session, command}, args...)
	out, err := e.cmd(full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux respawn-pane %s: %w (out=%q)", session, err, out)
	}
	return nil
}

func exitStderr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(ee.Stderr)
	}
	return ""
}
