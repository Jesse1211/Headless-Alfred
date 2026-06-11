# Multi-session Plan 2 — TmuxRunner abstraction + pty.stream reader with offset

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build two reusable, mockable pieces that Plan 3 will compose into the real `TmuxShell`: (a) a `TmuxRunner` interface that abstracts every `tmux ...` subprocess invocation behind a method call, and (b) a `StreamReader` that consumes a regular file produced by `tmux pipe-pane`, feeds bytes to a sentinel `Parser`, persists the byte offset, and truncates the file at safe (between-command) boundaries.

**Architecture:** Both pieces live in a new sub-package `internal/shell/tmuxio` so the existing `shell.Shell` and parser stay untouched. `TmuxRunner` is a Go interface with one production implementation (`ExecRunner`, shells out via `os/exec`) and one test implementation (`FakeRunner`, records calls and returns canned output). `StreamReader` reads from any `io.ReaderAt` plus a file path (for truncation), so tests can use a `bytes.Reader` wrapper without touching disk. Both pieces are pure libraries — they own no goroutines.

**Tech Stack:** stdlib only (`io`, `os`, `os/exec`, `bytes`, `path/filepath`, `errors`). Reuses `internal/shell/sentinel.go` (parser + Wrap function) unchanged.

**Spec sections covered:** §3 ("Why pty.stream is a regular file"), §4.1 (server bring-up commands as `TmuxRunner` methods), §4.4 (truncation), §4.7 (offset-based resume on reconciliation).

---

## File Structure

```
internal/shell/tmuxio/
├── runner.go              # NEW: TmuxRunner interface + ExecRunner (real)
├── runner_test.go         # NEW: ExecRunner tests skip if tmux binary missing
├── fake_runner.go         # NEW: FakeRunner for unit tests
├── stream_reader.go       # NEW: StreamReader (offset, truncate)
└── stream_reader_test.go  # NEW
```

Why a new sub-package: keeps `internal/shell` (the existing 350-line
package) free of "talks to tmux" surface area, and gives the tests a
clean import boundary. The existing parser is reused by importing
`github.com/jesseliu/headless-alfred/internal/shell` from the new
package.

---

## Task 1: TmuxRunner interface

The minimal surface we'll need to drive tmux for one session: create,
send keys, kill bash inside pane, respawn pane, list panes, kill
session, set/unset pipe, list sessions, set-option.

**Files:**
- Create: `internal/shell/tmuxio/runner.go`
- Create: `internal/shell/tmuxio/runner_test.go`

- [ ] **Step 1: Write the failing test for `ExecRunner.ListSessions` on a tmux-less host**

Create `internal/shell/tmuxio/runner_test.go`:

```go
package tmuxio

import (
	"os/exec"
	"testing"
)

func tmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

func TestExecRunner_ListSessions_NoServer_ReturnsEmpty(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux binary not on PATH")
	}
	// Use a brand-new socket path — guaranteed no server is running.
	sock := t.TempDir() + "/tmux.sock"
	r := NewExecRunner(sock)
	sessions, err := r.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions on a missing-server should not error, got %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty, got %+v", sessions)
	}
}

func TestExecRunner_CreateAndListSession_RoundTrip(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux binary not on PATH")
	}
	sock := t.TempDir() + "/tmux.sock"
	r := NewExecRunner(sock)
	t.Cleanup(func() {
		_ = r.KillSession("integration-test")
	})
	if err := r.NewSession("integration-test", "bash", "--noprofile", "--norc"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessions, err := r.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s == "integration-test" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("integration-test not in %v", sessions)
	}
}
```

- [ ] **Step 2: Run — confirm build error (types don't exist yet)**

Run: `go test ./internal/shell/tmuxio/ -count=1`
Expected: BUILD FAILS on `NewExecRunner`, `ExecRunner` undefined.

- [ ] **Step 3: Implement `TmuxRunner` interface + `ExecRunner`**

Create `internal/shell/tmuxio/runner.go`:

```go
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

	// SendKeys writes the literal string keys into the session's pane.
	// keys is sent verbatim — the caller adds trailing newlines if needed.
	SendKeys(session, keys string) error

	// PanePID returns the PID of the program currently running in the
	// session's (only) pane.
	PanePID(session string) (int, error)

	// PaneDead reports whether the pane's program has exited (set when
	// remain-on-exit is on).
	PaneDead(session string) (bool, error)

	// SetOption applies `tmux set-option -t <session> <name> <value>`.
	SetOption(session, name, value string) error

	// PipePane starts piping the pane's output to the given shell command.
	// Passing an empty cmd stops the pipe. The -o flag is always set so
	// this enables a NEW pipe, replacing any previously active one.
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
		// "no server running on <socket>" is a normal not-running state.
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(string(ee.Stderr), "no server running") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w (stderr=%q)", err, exitStderr(err))
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

func (e *ExecRunner) SendKeys(session, keys string) error {
	// The -l flag sends keys literally (no key-name interpretation).
	out, err := e.cmd("send-keys", "-t", session, "-l", keys).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys %s: %w (out=%q)", session, err, out)
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
```

- [ ] **Step 4: Run tests, confirm green (or properly skipped)**

Run: `go test ./internal/shell/tmuxio/ -count=1 -v`
Expected: Both `TestExecRunner_*` tests either PASS (if tmux is on PATH — local dev) or SKIP (if not — CI without tmux, until Plan 14 adds it).

- [ ] **Step 5: Commit**

```bash
git add internal/shell/tmuxio/runner.go internal/shell/tmuxio/runner_test.go
git commit -m "shell/tmuxio: TmuxRunner interface + ExecRunner (real tmux)"
```

---

## Task 2: FakeRunner — in-memory TmuxRunner for tests

A test double that records every call and lets tests stage canned
return values. Plan 3 (TmuxShell) and Plan 4 (Manager) lean heavily on this.

**Files:**
- Create: `internal/shell/tmuxio/fake_runner.go`
- Modify: `internal/shell/tmuxio/runner_test.go` (add tests for FakeRunner)

- [ ] **Step 1: Write the failing tests**

Append to `internal/shell/tmuxio/runner_test.go`:

```go
func TestFakeRunner_RecordsCalls(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("s1", "bash", "--noprofile", "--norc")
	_ = f.SendKeys("s1", "echo hello\n")
	_ = f.SetOption("s1", "remain-on-exit", "on")
	_ = f.PipePane("s1", "cat >> /tmp/x")

	got := f.Calls()
	if len(got) != 4 {
		t.Fatalf("want 4 calls, got %d: %+v", len(got), got)
	}
	if got[0].Method != "NewSession" || got[0].Args[0] != "s1" {
		t.Fatalf("call 0 = %+v", got[0])
	}
	if got[2].Method != "SetOption" || got[2].Args[2] != "on" {
		t.Fatalf("call 2 = %+v", got[2])
	}
}

func TestFakeRunner_ListSessions_Default_Empty(t *testing.T) {
	f := NewFakeRunner()
	names, err := f.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty, got %v", names)
	}
}

func TestFakeRunner_NewSession_AddsToListSessions(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	_ = f.NewSession("beta", "bash")
	names, _ := f.ListSessions()
	if len(names) != 2 {
		t.Fatalf("want 2, got %v", names)
	}
}

func TestFakeRunner_KillSession_RemovesFromList(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	_ = f.KillSession("alpha")
	names, _ := f.ListSessions()
	if len(names) != 0 {
		t.Fatalf("want empty, got %v", names)
	}
}

func TestFakeRunner_KillSession_NonExistent_Idempotent(t *testing.T) {
	f := NewFakeRunner()
	if err := f.KillSession("ghost"); err != nil {
		t.Fatalf("kill ghost: %v", err)
	}
}

func TestFakeRunner_PanePID_DefaultsToSyntheticValue(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	pid, err := f.PanePID("alpha")
	if err != nil {
		t.Fatalf("PanePID: %v", err)
	}
	if pid == 0 {
		t.Fatalf("expected non-zero synthetic pid")
	}
}

func TestFakeRunner_PaneDeadFlag(t *testing.T) {
	f := NewFakeRunner()
	_ = f.NewSession("alpha", "bash")
	dead, _ := f.PaneDead("alpha")
	if dead {
		t.Fatal("fresh session should not have dead pane")
	}
	f.MarkPaneDead("alpha")
	dead, _ = f.PaneDead("alpha")
	if !dead {
		t.Fatal("after MarkPaneDead, expected pane_dead=1")
	}
}

func TestFakeRunner_ErrorInjection(t *testing.T) {
	f := NewFakeRunner()
	f.NextErr("NewSession", errInjected)
	if err := f.NewSession("a", "bash"); err != errInjected {
		t.Fatalf("expected injected error, got %v", err)
	}
	// Error is one-shot.
	if err := f.NewSession("a", "bash"); err != nil {
		t.Fatalf("subsequent call should not error, got %v", err)
	}
}

var errInjected = errSentinel("injected")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/shell/tmuxio/ -run TestFakeRunner -count=1`
Expected: BUILD FAILS on `NewFakeRunner` undefined.

- [ ] **Step 3: Implement FakeRunner**

Create `internal/shell/tmuxio/fake_runner.go`:

```go
package tmuxio

import "sync"

// Call records one invocation of a TmuxRunner method.
type Call struct {
	Method string
	Args   []string
}

// FakeRunner is an in-memory TmuxRunner for tests. It tracks created
// sessions, records every call, and supports per-method one-shot
// error injection.
//
// FakeRunner is safe for concurrent use (the production TmuxShell
// calls some methods from a readLoop goroutine and others from the
// API handler goroutine).
type FakeRunner struct {
	mu       sync.Mutex
	calls    []Call
	sessions map[string]*fakeSession
	nextErr  map[string]error
	nextPID  int
}

type fakeSession struct {
	dead bool
	pid  int
}

func NewFakeRunner() *FakeRunner {
	return &FakeRunner{
		sessions: make(map[string]*fakeSession),
		nextErr:  make(map[string]error),
		nextPID:  10000,
	}
}

// Calls returns a snapshot of every recorded call, in order.
func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// NextErr stages an error to be returned by the NEXT call to method.
// One-shot: cleared after being returned once.
func (f *FakeRunner) NextErr(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextErr[method] = err
}

// MarkPaneDead flips the pane_dead flag for session. Used by tests to
// simulate bash exiting voluntarily.
func (f *FakeRunner) MarkPaneDead(session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[session]; ok {
		s.dead = true
	}
}

func (f *FakeRunner) takeErr(method string) error {
	if err, ok := f.nextErr[method]; ok {
		delete(f.nextErr, method)
		return err
	}
	return nil
}

func (f *FakeRunner) record(method string, args ...string) {
	f.calls = append(f.calls, Call{Method: method, Args: args})
}

func (f *FakeRunner) ListSessions() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListSessions")
	if err := f.takeErr("ListSessions"); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(f.sessions))
	for name := range f.sessions {
		out = append(out, name)
	}
	return out, nil
}

func (f *FakeRunner) NewSession(name, command string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	recArgs := append([]string{name, command}, args...)
	f.record("NewSession", recArgs...)
	if err := f.takeErr("NewSession"); err != nil {
		return err
	}
	f.sessions[name] = &fakeSession{pid: f.nextPID}
	f.nextPID++
	return nil
}

func (f *FakeRunner) KillSession(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("KillSession", name)
	if err := f.takeErr("KillSession"); err != nil {
		return err
	}
	delete(f.sessions, name)
	return nil
}

func (f *FakeRunner) SendKeys(session, keys string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SendKeys", session, keys)
	return f.takeErr("SendKeys")
}

func (f *FakeRunner) PanePID(session string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PanePID", session)
	if err := f.takeErr("PanePID"); err != nil {
		return 0, err
	}
	if s, ok := f.sessions[session]; ok {
		return s.pid, nil
	}
	return 0, nil
}

func (f *FakeRunner) PaneDead(session string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PaneDead", session)
	if err := f.takeErr("PaneDead"); err != nil {
		return false, err
	}
	if s, ok := f.sessions[session]; ok {
		return s.dead, nil
	}
	return false, nil
}

func (f *FakeRunner) SetOption(session, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SetOption", session, name, value)
	return f.takeErr("SetOption")
}

func (f *FakeRunner) PipePane(session, cmd string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PipePane", session, cmd)
	return f.takeErr("PipePane")
}

func (f *FakeRunner) RespawnPane(session, command string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	recArgs := append([]string{session, command}, args...)
	f.record("RespawnPane", recArgs...)
	if err := f.takeErr("RespawnPane"); err != nil {
		return err
	}
	if s, ok := f.sessions[session]; ok {
		s.dead = false
		s.pid = f.nextPID
		f.nextPID++
	}
	return nil
}
```

- [ ] **Step 4: Run all tmuxio tests**

Run: `go test ./internal/shell/tmuxio/ -count=1 -v`
Expected: All FakeRunner tests PASS. ExecRunner tests PASS (with tmux on PATH) or SKIP (without).

- [ ] **Step 5: Commit**

```bash
git add internal/shell/tmuxio/fake_runner.go internal/shell/tmuxio/runner_test.go
git commit -m "shell/tmuxio: FakeRunner for unit tests (call recording + error injection)"
```

---

## Task 3: StreamReader — offset-tracked file consumer

Reads a regular file (the `pty.stream` written by `tmux pipe-pane`),
feeds bytes to a sentinel parser, and persists the byte offset to a
companion file (`pty.offset`) after every successful read cycle. This
is what enables "Go-process restart resumes from the right byte"
(spec §4.7).

**Files:**
- Create: `internal/shell/tmuxio/stream_reader.go`
- Create: `internal/shell/tmuxio/stream_reader_test.go`

- [ ] **Step 1: Write the failing tests (offset-only behavior; no real parser yet)**

Create `internal/shell/tmuxio/stream_reader_test.go`:

```go
package tmuxio

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// recordedSink captures bytes the StreamReader hands off to the parser.
type recordedSink struct {
	chunks []string
}

func (r *recordedSink) Feed(b []byte) {
	r.chunks = append(r.chunks, string(b))
}

func TestStreamReader_ReadsFromOffsetZero_FreshFile(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	if err := os.WriteFile(stream, []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	sink := &recordedSink{}
	sr, err := NewStreamReader(stream, offsetFile, sink)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer sr.Close()
	n, err := sr.ReadOnce()
	if err != nil {
		t.Fatalf("ReadOnce: %v", err)
	}
	if n != 5 {
		t.Fatalf("want 5 bytes, got %d", n)
	}
	if len(sink.chunks) != 1 || sink.chunks[0] != "hello" {
		t.Fatalf("sink chunks: %v", sink.chunks)
	}
	// Offset persisted to disk.
	got := readOffset(t, offsetFile)
	if got != 5 {
		t.Fatalf("offset = %d, want 5", got)
	}
}

func TestStreamReader_ResumesFromPersistedOffset(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	if err := os.WriteFile(stream, []byte("hello world"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(offsetFile, []byte("6"), 0o600); err != nil {
		t.Fatalf("seed offset: %v", err)
	}
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	n, _ := sr.ReadOnce()
	if n != 5 {
		t.Fatalf("want 5 (the trailing 'world'), got %d", n)
	}
	if sink.chunks[0] != "world" {
		t.Fatalf("chunk: %q", sink.chunks[0])
	}
}

func TestStreamReader_ReadOnceReturnsZeroWhenNoNewBytes(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	_ = os.WriteFile(stream, []byte("x"), 0o600)
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	_, _ = sr.ReadOnce() // consume the 'x'
	n, err := sr.ReadOnce()
	if err != nil {
		t.Fatalf("ReadOnce: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 new bytes, got %d", n)
	}
	if len(sink.chunks) != 1 {
		t.Fatalf("sink should have 1 chunk total, got %d", len(sink.chunks))
	}
}

func TestStreamReader_MissingStream_CreatesEmpty(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	sink := &recordedSink{}
	sr, err := NewStreamReader(stream, offsetFile, sink)
	if err != nil {
		t.Fatalf("NewStreamReader on missing files should not error, got %v", err)
	}
	defer sr.Close()
	if _, err := os.Stat(stream); err != nil {
		t.Fatalf("stream file not created: %v", err)
	}
	n, _ := sr.ReadOnce()
	if n != 0 {
		t.Fatalf("fresh stream, want 0 bytes, got %d", n)
	}
}

func TestStreamReader_TruncateAtIdleBoundary(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	_ = os.WriteFile(stream, []byte("AAAAAAAA BBBBBBBB"), 0o600) // 17 bytes
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	_, _ = sr.ReadOnce()
	// Caller asks for truncate: keep nothing (everything has been consumed).
	if err := sr.TruncateConsumed(); err != nil {
		t.Fatalf("TruncateConsumed: %v", err)
	}
	info, err := os.Stat(stream)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("stream not truncated: size=%d", info.Size())
	}
	if got := readOffset(t, offsetFile); got != 0 {
		t.Fatalf("offset after truncate = %d, want 0", got)
	}
	// Next write through Append simulates tmux continuing to write.
	f, _ := os.OpenFile(stream, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write([]byte("XYZ"))
	_ = f.Close()
	n, _ := sr.ReadOnce()
	if n != 3 || sink.chunks[len(sink.chunks)-1] != "XYZ" {
		t.Fatalf("post-truncate read failed: n=%d chunks=%v", n, sink.chunks)
	}
}

func TestStreamReader_OffsetFileAtomicallyWritten(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	_ = os.WriteFile(stream, []byte("hi"), 0o600)
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	_, _ = sr.ReadOnce()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func readOffset(t *testing.T, path string) int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read offset: %v", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		t.Fatalf("parse offset %q: %v", data, err)
	}
	return n
}
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/shell/tmuxio/ -run TestStreamReader -count=1`
Expected: BUILD FAILS on `NewStreamReader` / `StreamReader` undefined.

- [ ] **Step 3: Implement StreamReader**

Create `internal/shell/tmuxio/stream_reader.go`:

```go
package tmuxio

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

// ParserSink is the minimal interface StreamReader needs to deliver bytes
// to a sentinel parser. The production parser lives in
// internal/shell.Parser; tests use a recorder.
type ParserSink interface {
	Feed(b []byte)
}

// StreamReader consumes a regular file that tmux pipe-pane appends to,
// hands each chunk to a ParserSink, and persists its byte offset so a
// crash + restart resumes from exactly where it left off.
//
// Not safe for concurrent use; call ReadOnce / TruncateConsumed from a
// single goroutine.
type StreamReader struct {
	streamPath string
	offsetPath string
	sink       ParserSink

	file   *os.File
	offset int64
}

// NewStreamReader opens streamPath (creating it if missing), seeks to
// the offset recorded in offsetPath (or 0 if missing), and returns the
// reader ready for ReadOnce.
//
// streamPath and offsetPath are created with 0o600 perms if absent.
func NewStreamReader(streamPath, offsetPath string, sink ParserSink) (*StreamReader, error) {
	// Make sure the stream file exists.
	if _, err := os.Stat(streamPath); os.IsNotExist(err) {
		f, err := os.OpenFile(streamPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create stream: %w", err)
		}
		_ = f.Close()
	} else if err != nil {
		return nil, fmt.Errorf("stat stream: %w", err)
	}

	// Load offset.
	var off int64
	if data, err := os.ReadFile(offsetPath); err == nil {
		parsed, perr := strconv.ParseInt(stringTrim(data), 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("parse offset %q: %w", data, perr)
		}
		off = parsed
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read offset: %w", err)
	}

	f, err := os.OpenFile(streamPath, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	if off > 0 {
		// Guard against the offset being beyond EOF (e.g., file was
		// truncated externally and offset is stale). Cap at file size.
		info, _ := f.Stat()
		if off > info.Size() {
			off = info.Size()
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("seek to %d: %w", off, err)
		}
	}
	return &StreamReader{
		streamPath: streamPath,
		offsetPath: offsetPath,
		sink:       sink,
		file:       f,
		offset:     off,
	}, nil
}

// Close releases the underlying file descriptor. Idempotent.
func (s *StreamReader) Close() error {
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Offset returns the current consumed byte position.
func (s *StreamReader) Offset() int64 { return s.offset }

// ReadOnce reads any bytes newly appended since the last call, feeds
// them to the sink, and persists the new offset to disk. Returns the
// number of bytes read (0 means no new data).
func (s *StreamReader) ReadOnce() (int, error) {
	buf := make([]byte, 32*1024)
	total := 0
	for {
		n, err := s.file.Read(buf)
		if n > 0 {
			s.sink.Feed(buf[:n])
			total += n
			s.offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("read stream: %w", err)
		}
		if n < len(buf) {
			break
		}
	}
	if total > 0 {
		if err := s.persistOffset(); err != nil {
			return total, err
		}
	}
	return total, nil
}

// TruncateConsumed empties the stream file and resets the in-memory
// and on-disk offsets to 0. Call only when the caller knows the
// parser is at an idle boundary (between commands) — otherwise output
// bytes can be lost.
//
// Note: tmux's pipe-pane writer still has the file open. The caller
// is responsible for sequencing this with PipePane(session, "") +
// PipePane(session, "<cmd>") to flush+stop+restart the pipe across
// the truncate, per spec §4.4. This function only handles the file +
// offset bookkeeping.
func (s *StreamReader) TruncateConsumed() error {
	if err := s.file.Close(); err != nil {
		return fmt.Errorf("close before truncate: %w", err)
	}
	if err := os.Truncate(s.streamPath, 0); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	f, err := os.OpenFile(s.streamPath, os.O_RDONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reopen after truncate: %w", err)
	}
	s.file = f
	s.offset = 0
	return s.persistOffset()
}

func (s *StreamReader) persistOffset() error {
	tmp := s.offsetPath + ".tmp"
	data := []byte(strconv.FormatInt(s.offset, 10))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write offset tmp: %w", err)
	}
	if err := os.Rename(tmp, s.offsetPath); err != nil {
		return fmt.Errorf("rename offset: %w", err)
	}
	return nil
}

func stringTrim(data []byte) string {
	s := string(data)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}
```

- [ ] **Step 4: Run StreamReader tests, confirm green**

Run: `go test ./internal/shell/tmuxio/ -run TestStreamReader -count=1 -v`
Expected: 6 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/shell/tmuxio/stream_reader.go internal/shell/tmuxio/stream_reader_test.go
git commit -m "shell/tmuxio: StreamReader with byte-offset resume + truncate"
```

---

## Task 4: Confirm full package green; race detector clean

The reader holds a single goroutine's worth of state but FakeRunner is
explicitly safe for concurrent use. Confirm the race detector finds
nothing.

- [ ] **Step 1: Run with race detector**

Run: `go test -race ./internal/shell/tmuxio/ -count=1 -v`
Expected: All PASS. No DATA RACE warnings.

- [ ] **Step 2: Confirm no placeholders in the new files**

Run: `grep -rE "TODO|FIXME|XXX" internal/shell/tmuxio/`
Expected: empty.

- [ ] **Step 3: Confirm test count**

Run: `go test ./internal/shell/tmuxio/ -count=1 -v 2>&1 | grep -c "^--- PASS\|^--- SKIP"`
Expected: 18 (2 ExecRunner that may SKIP without tmux + 9 FakeRunner + 6 StreamReader + 1 already counted via Step 4 of Task 3). Acceptable range: 16-18.

- [ ] **Step 4: No new commit — this is a verification pass**

---

## Plan 2 acceptance

- `go test -race ./internal/shell/tmuxio/` is green (PASS or SKIP for the two ExecRunner integration tests on tmux-less hosts).
- `TmuxRunner` interface exposes exactly the methods Plan 3 / 4 need:
  ListSessions, NewSession, KillSession, SendKeys, PanePID, PaneDead,
  SetOption, PipePane, RespawnPane.
- `FakeRunner` is safe for concurrent use and supports one-shot error
  injection per method.
- `StreamReader` resumes from a persisted offset, persists offset
  after every successful read, and can be told to truncate at idle
  boundaries.
- The rest of the codebase still compiles (we haven't changed any
  public types outside the new sub-package).

`go build ./...` may or may not pass depending on whether Plan 1 has
been merged. Plan 2 itself doesn't touch any file outside
`internal/shell/tmuxio/`.

---

## Plan 2 self-review checklist

- [ ] `grep -rE "TODO|FIXME|XXX" internal/shell/tmuxio/` is empty.
- [ ] `go vet ./internal/shell/tmuxio/` is clean.
- [ ] `go test -race -count=1 ./internal/shell/tmuxio/` is green.
- [ ] `git log --oneline | head -3` shows the three commits from this plan.
- [ ] FakeRunner method coverage matches TmuxRunner exactly (`grep "func (f \\*FakeRunner)" internal/shell/tmuxio/fake_runner.go | wc -l` equals the number of methods on the interface).
