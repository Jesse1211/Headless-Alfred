package claude

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Runner spawns and supervises one `claude -p` invocation per
// Prompt call. Each invocation is a one-shot: claude reads the
// prompt from argv (or stdin via flag), streams stream-json on
// stdout, exits when done. Runner.Prompt blocks only until the
// process is *spawned* — events flow asynchronously on the returned
// channel, which closes when stdout EOFs and the process exits.
//
// Multi-prompt continuity is achieved by passing the same
// `--session-id <uuid>` to each invocation; claude reads/writes its
// own transcript file at ~/.claude/projects/<cwd-encoded>/<uuid>.jsonl.
// Runner does not persist anything itself.
type Runner struct {
	// claudeBin is the path to the claude binary. Tests can swap it
	// for a fake binary that emits canned stream-json. Empty means
	// "use whatever's on PATH".
	claudeBin string

	// Logger receives spawn / exit telemetry. nil = slog.Default.
	Logger *slog.Logger
}

// NewRunner returns a Runner that invokes the `claude` binary on
// PATH.
func NewRunner() *Runner {
	return &Runner{}
}

// PromptOptions controls one `claude -p` invocation. Fields that
// matter for v1; we'll grow this struct as features land.
type PromptOptions struct {
	// SessionUUID, when non-empty, becomes --session-id <uuid>.
	// Empty means a fresh conversation; claude assigns a UUID we
	// capture from the first system/init event. See the long
	// comment in Prompt() for why --session-id and not --resume.
	SessionUUID string

	// CWD is the working directory claude inherits. Required for
	// --session-id to find the right transcript file on disk on
	// subsequent invocations (the cwd is part of the transcript
	// path hash, so a different cwd starts a new conversation
	// even for the same UUID).
	CWD string

	// Prompt is the user's text. Sent on the CLI's stdin as a
	// single message, terminated by EOF, to avoid shell escaping
	// issues and to support multi-line prompts.
	Prompt string

	// PermissionMode, if non-empty, is passed as --permission-mode.
	// Largely vestigial now that we always pass
	// --dangerously-skip-permissions: bypassPermissions disables the
	// CLI's built-in prompt, and our PreToolUse hook is the actual
	// gate (verified: hooks fire even under bypass mode). Left in
	// the struct so a future feature can override per-call without
	// reshaping the API.
	PermissionMode string
}

// PromptResult bundles the channel of streaming Events with handles
// to control the spawned process. The Events channel CLOSES when
// the process exits and stdout EOFs.
//
// The caller MUST read from Events until it closes (or call Stop and
// then drain), or the goroutine writing to it will block on the
// channel send when the buffer fills, leaving the process zombied.
type PromptResult struct {
	// Events streams parsed Events from claude's stdout. Closes on
	// process exit + EOF.
	Events <-chan Event

	// Wait blocks until the process has fully exited and the parser
	// goroutine has drained. Returns the process's exit error, if any.
	Wait func() error

	// Stop sends SIGINT to the claude process. Safe to call after
	// Wait returns (no-op). Safe to call concurrently from another
	// goroutine.
	Stop func()
}

// Prompt forks `claude -p ...` with the given options and returns
// a PromptResult. Errors here mean we couldn't even start the
// process (binary missing, cwd invalid, etc.). Errors during the
// conversation surface on the Events channel as UnknownEvents or a
// Result with IsError=true; the caller must inspect those.
func (r *Runner) Prompt(ctx context.Context, opts PromptOptions) (*PromptResult, error) {
	if opts.CWD == "" {
		return nil, fmt.Errorf("PromptOptions.CWD required")
	}
	if opts.Prompt == "" {
		return nil, fmt.Errorf("PromptOptions.Prompt required")
	}

	bin := r.claudeBin
	if bin == "" {
		bin = "claude"
	}
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		// Skip the CLI's built-in "Run this Bash command? Y/N" prompts —
		// in -p (headless) mode they would deadlock because there's no
		// TTY. Our PreToolUse hook is still invoked and is the single
		// source of truth for permissions (bypassPermissions does NOT
		// disable hooks, verified empirically).
		"--dangerously-skip-permissions",
	}
	if opts.SessionUUID != "" {
		// The CLI is asymmetric here:
		//   --session-id <uuid>: only valid the FIRST time. On a second
		//     invocation with the same UUID it errors with
		//     "Session ID <uuid> is already in use." and exits 1.
		//   --resume <uuid>: only valid AFTER a transcript exists. On
		//     the first invocation it errors with "No conversation
		//     found with session ID <uuid>".
		// So we probe the transcript path and choose the right flag.
		// The transcript lives at
		//   ~/.claude/projects/<cwd-with-/-as--><uuid>.jsonl
		// where <cwd-with-/-as--> is the absolute cwd with every '/'
		// replaced by '-' (so a leading '-' shows up too).
		if transcriptExists(opts.CWD, opts.SessionUUID) {
			args = append(args, "--resume", opts.SessionUUID)
		} else {
			args = append(args, "--session-id", opts.SessionUUID)
		}
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}

	// We pipe the prompt on stdin instead of putting it in argv so
	// that multi-line prompts and shell-special characters Just Work.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.CWD
	// Inherit the env (so ANTHROPIC_API_KEY etc. flow through), but
	// scrub anything we don't want claude to pick up later — we
	// leave that to a future tightening pass. For v1, inherit all.
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	// Set the process group so we can SIGINT just claude (and its
	// children, e.g. tools claude spawns) without taking down the
	// alfred-server. On Linux, setpgid(0,0) puts the new process in
	// its own pgrp.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("claude.Runner: spawned",
		"pid", cmd.Process.Pid,
		"cwd", opts.CWD,
		"resume", opts.SessionUUID != "")

	// Send the prompt and close stdin so claude proceeds.
	go func() {
		defer stdin.Close()
		_, _ = io.WriteString(stdin, opts.Prompt)
	}()

	// Drain stderr in the background; log non-empty content so
	// auth failures and similar are visible. We don't surface it
	// on the Events channel for v1 — the CLI tends to put real
	// errors into a stream-json result event with is_error:true.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				logger.Warn("claude.Runner stderr", "data", string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()

	events := ParseStream(stdout)

	// We need to know when the process has exited AND the parser
	// has drained. Wait() blocks for both.
	var (
		waitOnce sync.Once
		waitErr  error
		waitWG   sync.WaitGroup
	)
	waitWG.Add(1)
	go func() {
		defer waitWG.Done()
		waitErr = cmd.Wait()
		if waitErr != nil {
			logger.Info("claude.Runner: process exited with error",
				"pid", cmd.Process.Pid, "err", waitErr)
		}
	}()

	pr := &PromptResult{
		Events: events,
		Wait: func() error {
			waitWG.Wait()
			return waitErr
		},
		Stop: func() {
			waitOnce.Do(func() {
				if cmd.Process == nil {
					return
				}
				// Signal the whole process group.
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
			})
		},
	}
	return pr, nil
}

// transcriptExists reports whether the on-disk transcript file for
// (cwd, uuid) already exists. Used to pick between --session-id
// (first invocation, no file) and --resume (subsequent, file
// exists).
//
// Claude stores transcripts at:
//
//	$HOME/.claude/projects/<cwd-flattened>/<uuid>.jsonl
//
// where <cwd-flattened> is the absolute cwd with every '/'
// replaced by '-'. An absolute path like "/Users/jesseliu" becomes
// "-Users-jesseliu" (note the leading '-' from the leading '/').
//
// We don't try to be clever about $XDG_DATA_HOME or alternative
// claude config locations — the CLI's own default is hardcoded to
// $HOME/.claude/projects too. If the user has set up a non-default
// location, the worst case is we choose --session-id when we
// should have chosen --resume and get a misleading error one time
// at the API boundary.
func transcriptExists(cwd, uuid string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	flat := strings.ReplaceAll(cwd, "/", "-")
	path := filepath.Join(home, ".claude", "projects", flat, uuid+".jsonl")
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}
