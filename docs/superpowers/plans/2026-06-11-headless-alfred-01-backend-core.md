# Headless Alfred — Plan 1: Backend Core

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the three core Go packages (`shell`, `store`, `auth`) that all subsequent layers depend on. No HTTP. No frontend.

**Architecture:** Each package is independently testable with no cross-dependencies. `shell` owns a single bash via PTY and exposes a fan-out broadcaster. `store` reads/writes per-command JSON files atomically. `auth` checks static credentials and a static token with a per-IP login rate limiter.

**Tech Stack:** Go 1.22+, `github.com/creack/pty`, `github.com/oklog/ulid/v2`, stdlib only otherwise (`log/slog`, `crypto/subtle`).

**Spec sections covered:** §3.1 (sentinel framing), §4 (shell/store/auth modules), §5 (broadcaster fan-out), §7 (file formats, atomicity, sweep), §8 (auth), §10 (rate limit).

---

## File Structure

```
Headless-Alfred/
├── go.mod
├── go.sum
├── .gitignore
├── Makefile                        # convenience targets
└── internal/
    ├── shell/
    │   ├── broadcaster.go          # fan-out with per-subscriber ring buffer
    │   ├── broadcaster_test.go
    │   ├── sentinel.go             # marker constants + parser
    │   ├── sentinel_test.go
    │   ├── shell.go                # bash + PTY lifecycle, Write, Stop, Subscribe
    │   └── shell_integration_test.go   # real bash subprocess
    ├── store/
    │   ├── record.go               # types
    │   ├── store.go                # Save, Get, List, AppendOutput, Sweep
    │   └── store_test.go
    └── auth/
        ├── auth.go                 # CheckLogin, VerifyToken, env loading
        ├── ratelimit.go            # per-IP token bucket
        ├── auth_test.go
        └── ratelimit_test.go
```

---

## Task 0: Bootstrap project

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `Makefile`

- [ ] **Step 0.1: Initialize Go module**

Run from repo root:
```bash
go mod init github.com/jesseliu/headless-alfred
```

Expected: creates `go.mod` with module path and Go version.

- [ ] **Step 0.2: Add dependencies**

```bash
go get github.com/creack/pty
go get github.com/oklog/ulid/v2
```

Expected: `go.mod` and `go.sum` updated.

- [ ] **Step 0.3: Write `.gitignore`**

Create `.gitignore`:
```
# Build artifacts
/bin/
/internal/static/dist/

# Go
*.test
*.out
/coverage.out

# Node (for later plans, harmless now)
/web/node_modules/
/web/dist/

# Editor
.idea/
.vscode/
*.swp
.DS_Store

# Local-only secrets
deploy/manifests/secret.yaml
```

- [ ] **Step 0.4: Write `Makefile` (minimal for now)**

Create `Makefile`:
```makefile
.PHONY: test test-unit test-integration tidy

test: test-unit test-integration

test-unit:
	go test -race -short ./internal/...

test-integration:
	go test -race -run Integration ./internal/...

tidy:
	go mod tidy
```

- [ ] **Step 0.5: Commit**

```bash
git add go.mod go.sum .gitignore Makefile
git commit -m "chore: bootstrap Go module and conventions"
```

---

## Task 1: Broadcaster — failing test first

**Files:**
- Create: `internal/shell/broadcaster_test.go`
- Create: `internal/shell/broadcaster.go`

The broadcaster fans out byte chunks to N subscribers. Each subscriber has its own bounded channel; if a subscriber falls behind, it gets a `DroppedBytes` marker instead of blocking the publisher.

- [ ] **Step 1.1: Write the failing tests**

Create `internal/shell/broadcaster_test.go`:
```go
package shell

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestBroadcaster_DeliversToSubscriber(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(64)
	b.Publish([]byte("hello"))

	select {
	case msg := <-sub.C:
		if msg.Drop {
			t.Fatalf("unexpected drop marker")
		}
		if !bytes.Equal(msg.Bytes, []byte("hello")) {
			t.Fatalf("got %q want %q", msg.Bytes, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestBroadcaster_FanOutToMultipleSubscribers(t *testing.T) {
	b := NewBroadcaster()
	s1 := b.Subscribe(64)
	s2 := b.Subscribe(64)

	b.Publish([]byte("x"))

	for _, s := range []*Subscriber{s1, s2} {
		select {
		case msg := <-s.C:
			if !bytes.Equal(msg.Bytes, []byte("x")) {
				t.Fatalf("got %q", msg.Bytes)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
}

func TestBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(64)
	sub.Close()

	// Channel must be closed eventually.
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-sub.C:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel not closed after Close()")
		}
	}
}

func TestBroadcaster_SlowSubscriberGetsDropMarker(t *testing.T) {
	b := NewBroadcaster()
	slow := b.Subscribe(1) // tiny buffer
	fast := b.Subscribe(1024)

	// Publish more than slow can buffer.
	for i := 0; i < 100; i++ {
		b.Publish([]byte("chunk"))
	}

	// Fast subscriber should still get a delivery (publisher not blocked).
	select {
	case <-fast.C:
	case <-time.After(time.Second):
		t.Fatal("fast subscriber starved by slow one")
	}

	// Slow subscriber should eventually see a Drop marker.
	gotDrop := false
	deadline := time.After(time.Second)
loop:
	for {
		select {
		case msg, ok := <-slow.C:
			if !ok {
				break loop
			}
			if msg.Drop {
				gotDrop = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !gotDrop {
		t.Fatal("expected drop marker for slow subscriber, got none")
	}
}

func TestBroadcaster_ConcurrentPublishSafe(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(10000)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Publish([]byte("x"))
			}
		}()
	}
	wg.Wait()
	// Drain
	drained := 0
	for {
		select {
		case <-sub.C:
			drained++
		default:
			if drained < 1000 {
				t.Fatalf("expected 1000 messages, drained %d", drained)
			}
			return
		}
	}
}
```

- [ ] **Step 1.2: Run tests to verify they fail**

```bash
go test -race ./internal/shell/
```

Expected: fail with "undefined: Broadcaster" / "undefined: NewBroadcaster" etc.

- [ ] **Step 1.3: Implement Broadcaster**

Create `internal/shell/broadcaster.go`:
```go
package shell

import "sync"

// Message is one delivery on a subscriber's channel.
// Either Bytes is set (a chunk of output) or Drop is true (the subscriber
// missed bytes because it couldn't keep up).
type Message struct {
	Bytes []byte
	Drop  bool
}

// Subscriber receives published bytes on C until Close() is called.
type Subscriber struct {
	C      chan Message
	closed bool
	mu     sync.Mutex
	b      *Broadcaster
}

// Close removes this subscriber from the broadcaster and closes C.
// Safe to call multiple times.
func (s *Subscriber) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.b.unsubscribe(s)
}

// Broadcaster delivers each Publish to every active Subscriber.
// A Subscriber whose channel is full receives a single Drop marker instead
// of the chunk; the publisher never blocks on a slow subscriber.
type Broadcaster struct {
	mu   sync.RWMutex
	subs map[*Subscriber]bool
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[*Subscriber]bool)}
}

func (b *Broadcaster) Subscribe(buffer int) *Subscriber {
	if buffer < 1 {
		buffer = 1
	}
	s := &Subscriber{
		C: make(chan Message, buffer),
		b: b,
	}
	b.mu.Lock()
	b.subs[s] = false // false = not currently in drop state
	b.mu.Unlock()
	return s
}

func (b *Broadcaster) unsubscribe(s *Subscriber) {
	b.mu.Lock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		close(s.C)
	}
	b.mu.Unlock()
}

// Publish copies bytes (caller may reuse the slice) and sends to every
// subscriber. Slow subscribers get at most one Drop marker per backlog
// episode; subsequent overflows are silently coalesced into that marker
// until the subscriber catches up.
func (b *Broadcaster) Publish(p []byte) {
	if len(p) == 0 {
		return
	}
	cp := make([]byte, len(p))
	copy(cp, p)

	b.mu.Lock()
	defer b.mu.Unlock()
	for sub, inDrop := range b.subs {
		if inDrop {
			// Already sent a drop marker; wait for subscriber to catch up
			// (channel emptied) before resuming normal sends.
			if len(sub.C) == 0 {
				b.subs[sub] = false
				select {
				case sub.C <- Message{Bytes: cp}:
				default:
					// Still full somehow; stay in drop state.
					b.subs[sub] = true
				}
			}
			continue
		}
		select {
		case sub.C <- Message{Bytes: cp}:
		default:
			// Buffer full — send drop marker if there's room, then mark inDrop.
			select {
			case sub.C <- Message{Drop: true}:
			default:
			}
			b.subs[sub] = true
		}
	}
}
```

- [ ] **Step 1.4: Run tests to verify they pass**

```bash
go test -race ./internal/shell/
```

Expected: all 5 tests PASS.

- [ ] **Step 1.5: Commit**

```bash
git add internal/shell/broadcaster.go internal/shell/broadcaster_test.go
git commit -m "feat(shell): broadcaster with per-subscriber drop semantics"
```

---

## Task 2: Sentinel constants and parser

The shell wraps every user command with sentinel marker lines around its execution. The parser is a state machine over the raw PTY stream.

**Files:**
- Create: `internal/shell/sentinel.go`
- Create: `internal/shell/sentinel_test.go`

- [ ] **Step 2.1: Write the failing tests**

Create `internal/shell/sentinel_test.go`:
```go
package shell

import (
	"bytes"
	"testing"
)

func TestSentinelParser_PassesThroughCommandBody(t *testing.T) {
	p := NewParser("NONCE")
	var events []ParseEvent
	p.OnEvent = func(e ParseEvent) { events = append(events, e) }

	input := []byte("\x1eALFRED_START_NONCE 01HAB /home/user\x1e\nhello world\n\x1eALFRED_END_NONCE 01HAB 0\x1e\n")
	p.Feed(input)

	if len(events) < 3 {
		t.Fatalf("want at least 3 events (start, chunk, end), got %d: %+v", len(events), events)
	}
	if events[0].Kind != EventStart || events[0].CmdID != "01HAB" || events[0].Cwd != "/home/user" {
		t.Fatalf("bad start event: %+v", events[0])
	}
	// Find the chunk event(s) and concatenate.
	var body bytes.Buffer
	for _, e := range events {
		if e.Kind == EventChunk {
			body.Write(e.Bytes)
		}
	}
	if body.String() != "hello world\n" {
		t.Fatalf("body=%q want %q", body.String(), "hello world\n")
	}
	last := events[len(events)-1]
	if last.Kind != EventEnd || last.CmdID != "01HAB" || last.ExitCode != 0 {
		t.Fatalf("bad end event: %+v", last)
	}
}

func TestSentinelParser_HandlesSentinelSplitAcrossFeeds(t *testing.T) {
	p := NewParser("NONCE")
	var events []ParseEvent
	p.OnEvent = func(e ParseEvent) { events = append(events, e) }

	input := []byte("\x1eALFRED_START_NONCE 01HAB /tmp\x1e\nx\n\x1eALFRED_END_NONCE 01HAB 7\x1e\n")
	// Feed one byte at a time — parser must buffer partial sentinels.
	for i := range input {
		p.Feed(input[i : i+1])
	}

	if events[0].Kind != EventStart {
		t.Fatalf("want start first, got %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Kind != EventEnd || last.ExitCode != 7 {
		t.Fatalf("bad end event: %+v", last)
	}
}

func TestSentinelParser_ExitCodeNonZero(t *testing.T) {
	p := NewParser("NONCE")
	var endEvt ParseEvent
	p.OnEvent = func(e ParseEvent) {
		if e.Kind == EventEnd {
			endEvt = e
		}
	}
	p.Feed([]byte("\x1eALFRED_START_NONCE A /\x1e\n\x1eALFRED_END_NONCE A 137\x1e\n"))
	if endEvt.ExitCode != 137 {
		t.Fatalf("exit code = %d, want 137", endEvt.ExitCode)
	}
}

func TestSentinelParser_IgnoresBytesOutsideCommand(t *testing.T) {
	p := NewParser("NONCE")
	var chunks [][]byte
	p.OnEvent = func(e ParseEvent) {
		if e.Kind == EventChunk {
			chunks = append(chunks, append([]byte{}, e.Bytes...))
		}
	}
	// Bytes before any START must be dropped (this is bash startup noise).
	p.Feed([]byte("bash welcome banner\n"))
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks before START, got %d", len(chunks))
	}
}

func TestWrap_ContainsBothSentinels(t *testing.T) {
	wrapped := Wrap("NONCE", "01HAB", "ls -la")
	if !bytes.Contains([]byte(wrapped), []byte("ALFRED_START_NONCE 01HAB")) {
		t.Fatalf("missing start sentinel: %q", wrapped)
	}
	if !bytes.Contains([]byte(wrapped), []byte("ALFRED_END_NONCE 01HAB")) {
		t.Fatalf("missing end sentinel: %q", wrapped)
	}
	if !bytes.Contains([]byte(wrapped), []byte("ls -la")) {
		t.Fatalf("missing user command: %q", wrapped)
	}
}
```

- [ ] **Step 2.2: Run tests to verify they fail**

```bash
go test -race -run Sentinel ./internal/shell/
go test -race -run Wrap ./internal/shell/
```

Expected: fail with "undefined: NewParser" / "undefined: Wrap" etc.

- [ ] **Step 2.3: Implement sentinel.go**

Create `internal/shell/sentinel.go`:
```go
package shell

import (
	"bytes"
	"fmt"
	"strconv"
)

// RS is the ASCII Record Separator (0x1e). Used to bracket sentinel lines so
// they're vanishingly unlikely to appear naturally in command output.
const RS = '\x1e'

type EventKind int

const (
	EventStart EventKind = iota
	EventChunk
	EventEnd
)

type ParseEvent struct {
	Kind     EventKind
	CmdID    string
	Cwd      string // only on EventStart
	Bytes    []byte // only on EventChunk
	ExitCode int    // only on EventEnd
}

// Wrap returns the byte sequence to write to bash's stdin so that the user
// command runs sandwiched between START and END sentinels.
//
// Format on START line (sent verbatim to bash):
//     printf '\x1eALFRED_START_<nonce> <cmdID> %s\x1e\n' "$PWD"
// On END line:
//     printf '\x1eALFRED_END_<nonce> <cmdID> %d\x1e\n' "$__alfred_ec"
func Wrap(nonce, cmdID, userCmd string) string {
	return fmt.Sprintf(
		"printf '\\x1eALFRED_START_%s %s %%s\\x1eX\\n' \"$PWD\"\n%s\n__alfred_ec=$?\nprintf '\\x1eALFRED_END_%s %s %%d\\x1eX\\n' \"$__alfred_ec\"\n",
		nonce, cmdID, userCmd, nonce, cmdID,
	)
	// Note: an "X" is appended after the closing RS so the parser sees a
	// distinct terminator even if the printf is followed by no newline.
	// The trailing \n is normalised away below.
}

// Parser feeds bytes from the PTY and emits events.
type Parser struct {
	nonce   string
	OnEvent func(ParseEvent)

	buf   bytes.Buffer
	state parseState
	cur   activeCmd
}

type parseState int

const (
	stateOutside parseState = iota // bytes belong to no command — discard
	stateInside                    // bytes belong to cur.id — emit as chunks
)

type activeCmd struct {
	id string
}

func NewParser(nonce string) *Parser {
	return &Parser{nonce: nonce, state: stateOutside}
}

// Feed appends bytes from the PTY and processes any complete sentinel lines.
// Bytes that are part of a sentinel are consumed; bytes belonging to the
// current command body are forwarded via OnEvent(EventChunk).
func (p *Parser) Feed(b []byte) {
	p.buf.Write(b)
	p.process()
}

func (p *Parser) process() {
	for {
		data := p.buf.Bytes()
		// Look for next RS.
		idx := bytes.IndexByte(data, RS)
		if idx < 0 {
			// No RS: everything is plain body (or pre-START noise).
			if p.state == stateInside && len(data) > 0 {
				p.emit(EventChunk, p.cur.id, "", append([]byte{}, data...), 0)
			}
			p.buf.Reset()
			return
		}
		// Bytes before RS are body (if inside) or discarded (if outside).
		if idx > 0 {
			if p.state == stateInside {
				p.emit(EventChunk, p.cur.id, "", append([]byte{}, data[:idx]...), 0)
			}
		}
		// Now starting at the RS, look for the closing RS that ends the sentinel.
		rest := data[idx:]
		end := bytes.IndexByte(rest[1:], RS)
		if end < 0 {
			// Sentinel not yet complete; keep RS and what follows in buf, retry on next feed.
			p.buf.Reset()
			p.buf.Write(rest)
			return
		}
		// rest[0..end+1] is the sentinel line including both RS bytes.
		sentinel := string(rest[1 : end+1]) // between the two RS bytes
		// Consume the sentinel plus the "X" terminator char and optional newline.
		consumed := end + 2 // RS .. RS
		// Skip terminator byte ('X') and any \n that follows it.
		tail := rest[consumed:]
		extra := 0
		if len(tail) > 0 && tail[0] == 'X' {
			extra++
		}
		if len(tail) > extra && tail[extra] == '\n' {
			extra++
		}
		p.handleSentinel(sentinel)
		// Rebuild buffer: drop everything up to and including consumed+extra.
		newBuf := append([]byte{}, rest[consumed+extra:]...)
		p.buf.Reset()
		p.buf.Write(newBuf)
	}
}

func (p *Parser) handleSentinel(s string) {
	// Expected forms:
	//   ALFRED_START_<nonce> <cmdID> <cwd>
	//   ALFRED_END_<nonce> <cmdID> <exitCode>
	startPrefix := "ALFRED_START_" + p.nonce + " "
	endPrefix := "ALFRED_END_" + p.nonce + " "
	switch {
	case len(s) > len(startPrefix) && s[:len(startPrefix)] == startPrefix:
		rest := s[len(startPrefix):]
		// rest = "<cmdID> <cwd>"
		sp := bytes.IndexByte([]byte(rest), ' ')
		if sp < 0 {
			return
		}
		id := rest[:sp]
		cwd := rest[sp+1:]
		p.cur = activeCmd{id: id}
		p.state = stateInside
		p.emit(EventStart, id, cwd, nil, 0)
	case len(s) > len(endPrefix) && s[:len(endPrefix)] == endPrefix:
		rest := s[len(endPrefix):]
		sp := bytes.IndexByte([]byte(rest), ' ')
		if sp < 0 {
			return
		}
		id := rest[:sp]
		ec, err := strconv.Atoi(rest[sp+1:])
		if err != nil {
			ec = -1
		}
		p.emit(EventEnd, id, "", nil, ec)
		p.cur = activeCmd{}
		p.state = stateOutside
	}
}

func (p *Parser) emit(k EventKind, id, cwd string, body []byte, ec int) {
	if p.OnEvent == nil {
		return
	}
	p.OnEvent(ParseEvent{Kind: k, CmdID: id, Cwd: cwd, Bytes: body, ExitCode: ec})
}
```

- [ ] **Step 2.4: Run tests to verify they pass**

```bash
go test -race -run "Sentinel|Wrap" ./internal/shell/
```

Expected: all 5 tests PASS.

- [ ] **Step 2.5: Commit**

```bash
git add internal/shell/sentinel.go internal/shell/sentinel_test.go
git commit -m "feat(shell): sentinel-based output framing"
```

---

## Task 3: Shell — PTY lifecycle and Subscribe

**Files:**
- Create: `internal/shell/shell.go`

This task wires Broadcaster + Parser to a real PTY-backed bash. Pure-unit testing PTY behavior is painful, so the integration test in Task 4 is the load-bearing test. This task focuses on writing the file with clear interfaces; the integration test will catch wiring bugs.

- [ ] **Step 3.1: Implement shell.go**

Create `internal/shell/shell.go`:
```go
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
	"syscall"
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
}

// RunningCommand is the state of a command currently executing.
type RunningCommand struct {
	ID         string
	Command    string
	Cwd        string
	StartedAt  time.Time
	Buffer     []byte // copy of all bytes so far, capped at MaxBufferBytes
	Truncated  bool
}

// Shell owns one persistent bash and the parser/broadcaster pipeline.
type Shell struct {
	logger *slog.Logger
	nonce  string

	mu         sync.Mutex
	cmd        *exec.Cmd
	pty        *os.File
	parser     *Parser
	available  bool
	currentCmd *RunningCommand

	cmdEvents  chan CommandEvent // emitted to subscribers via dedicated broadcaster
	rawBcast   *Broadcaster      // raw chunks before sentinel parsing — used by no one externally
	evtBcast   *EventBroadcaster // typed events
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
	// Mark current command as interrupted at the event layer.
	if s.currentCmd != nil {
		evt := CommandEvent{Ended: &EndedEvent{
			CmdID:      s.currentCmd.ID,
			ExitCode:   -1,
			FinishedAt: time.Now().UTC(),
			Truncated:  s.currentCmd.Truncated,
		}}
		s.currentCmd = nil
		s.mu.Unlock()
		s.evtBcast.Publish(evt)
	} else {
		s.mu.Unlock()
	}
	// Attempt one restart.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmd = nil
	s.pty = nil
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

// Stop sends SIGINT to bash to interrupt the current command.
// No-op if nothing is running.
func (s *Shell) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentCmd == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(s.cmd.Process.Pid, syscall.SIGINT)
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
		s.currentCmd = nil
		s.mu.Unlock()
		s.evtBcast.Publish(CommandEvent{Ended: &EndedEvent{
			CmdID:      e.CmdID,
			ExitCode:   e.ExitCode,
			FinishedAt: time.Now().UTC(),
			Truncated:  truncated,
		}})
	}
}
```

- [ ] **Step 3.2: Implement event broadcaster (typed sibling of raw Broadcaster)**

Create `internal/shell/event_broadcaster.go`:
```go
package shell

import "sync"

type EventSubscriber struct {
	C      chan CommandEvent
	closed bool
	mu     sync.Mutex
	b      *EventBroadcaster
}

func (s *EventSubscriber) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.b.unsubscribe(s)
}

type EventBroadcaster struct {
	mu   sync.RWMutex
	subs map[*EventSubscriber]struct{}
}

func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{subs: make(map[*EventSubscriber]struct{})}
}

func (b *EventBroadcaster) Subscribe(buffer int) *EventSubscriber {
	if buffer < 1 {
		buffer = 1
	}
	s := &EventSubscriber{C: make(chan CommandEvent, buffer), b: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *EventBroadcaster) unsubscribe(s *EventSubscriber) {
	b.mu.Lock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		close(s.C)
	}
	b.mu.Unlock()
}

func (b *EventBroadcaster) Publish(e CommandEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for sub := range b.subs {
		select {
		case sub.C <- e:
		default:
			// Drop event for a slow subscriber. Event loss for chunks is
			// covered by the Drop flag on the Chunk itself when buffered;
			// for Started/Ended events, slow subscribers may miss them —
			// acceptable because no use case requires strict event delivery
			// (UI does a full state read on reconnect).
		}
	}
}
```

- [ ] **Step 3.3: Make it compile**

```bash
go build ./internal/shell/
```

Expected: no errors. If `creack/pty` is missing, run `go get github.com/creack/pty`.

- [ ] **Step 3.4: Commit**

```bash
git add internal/shell/shell.go internal/shell/event_broadcaster.go
git commit -m "feat(shell): PTY-backed bash with parser, restart, broadcaster"
```

---

## Task 4: Shell integration test (real bash)

**Files:**
- Create: `internal/shell/shell_integration_test.go`

These tests launch real bash. They're behind the `Integration` test name filter (run via `make test-integration`). They must be skipped if bash is unavailable.

- [ ] **Step 4.1: Write the integration tests**

Create `internal/shell/shell_integration_test.go`:
```go
//go:build !windows

package shell

import (
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
}

func newTestShell(t *testing.T) *Shell {
	t.Helper()
	requireBash(t)
	s := NewShell(slog.Default())
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort kill.
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
	return s
}

func runAndCollect(t *testing.T, s *Shell, cmdID, userCmd string) (string, int) {
	t.Helper()
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()

	if err := s.Write(cmdID, userCmd); err != nil {
		t.Fatalf("write: %v", err)
	}

	var output strings.Builder
	deadline := time.After(15 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Chunk != nil && evt.Chunk.CmdID == cmdID {
				output.Write(evt.Chunk.Bytes)
			}
			if evt.Ended != nil && evt.Ended.CmdID == cmdID {
				return output.String(), evt.Ended.ExitCode
			}
		case <-deadline:
			t.Fatalf("timeout waiting for cmd to end. partial output: %q", output.String())
		}
	}
}

func TestIntegration_EchoReturnsOutputAndZeroExit(t *testing.T) {
	s := newTestShell(t)
	out, ec := runAndCollect(t, s, "cmd-echo", "echo hello-world")
	if ec != 0 {
		t.Fatalf("exit code = %d, want 0", ec)
	}
	if !strings.Contains(out, "hello-world") {
		t.Fatalf("output missing expected text: %q", out)
	}
}

func TestIntegration_NonZeroExitCodePropagates(t *testing.T) {
	s := newTestShell(t)
	_, ec := runAndCollect(t, s, "cmd-fail", "false")
	if ec == 0 {
		t.Fatalf("expected non-zero exit, got 0")
	}
}

func TestIntegration_CDPersistsAcrossCommands(t *testing.T) {
	s := newTestShell(t)
	runAndCollect(t, s, "cd-cmd", "cd /tmp")
	out, ec := runAndCollect(t, s, "pwd-cmd", "pwd")
	if ec != 0 {
		t.Fatalf("pwd failed: ec=%d out=%q", ec, out)
	}
	if !strings.Contains(out, "/tmp") {
		t.Fatalf("pwd output = %q, want contains /tmp", out)
	}
}

func TestIntegration_CWDCapturedFromStartSentinel(t *testing.T) {
	s := newTestShell(t)
	runAndCollect(t, s, "cd-cmd", "cd /tmp")

	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("after-cd", "echo done"); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Started != nil && evt.Started.CmdID == "after-cd" {
				if evt.Started.Cwd != "/tmp" {
					t.Fatalf("captured cwd = %q, want /tmp", evt.Started.Cwd)
				}
				return
			}
		case <-deadline:
			t.Fatalf("no started event")
		}
	}
}

func TestIntegration_ConcurrentWriteRejectedAsBusy(t *testing.T) {
	s := newTestShell(t)
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("slow", "sleep 1; echo done"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.Write("second", "echo nope"); err != ErrBusy {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	// Drain until slow ends.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Ended != nil && evt.Ended.CmdID == "slow" {
				return
			}
		case <-deadline:
			t.Fatalf("slow never ended")
		}
	}
}

func TestIntegration_StopSendsSIGINT(t *testing.T) {
	s := newTestShell(t)
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("long", "sleep 30; echo done"); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Give bash a moment to actually start sleep.
	time.Sleep(200 * time.Millisecond)
	s.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub.C:
			if evt.Ended != nil && evt.Ended.CmdID == "long" {
				if evt.Ended.ExitCode == 0 {
					t.Fatalf("stop did not produce non-zero exit code")
				}
				return
			}
		case <-deadline:
			t.Fatalf("stop did not terminate command")
		}
	}
}

func TestIntegration_StreamingOutputArrivesIncrementally(t *testing.T) {
	s := newTestShell(t)
	sub, cancel := s.SubscribeEvents(256)
	defer cancel()
	if err := s.Write("stream", "for i in 1 2 3; do echo $i; sleep 0.4; done"); err != nil {
		t.Fatalf("write: %v", err)
	}
	var chunkTimes []time.Time
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case evt := <-sub.C:
			if evt.Chunk != nil && evt.Chunk.CmdID == "stream" {
				chunkTimes = append(chunkTimes, time.Now())
			}
			if evt.Ended != nil && evt.Ended.CmdID == "stream" {
				break loop
			}
		case <-deadline:
			t.Fatalf("timeout. got %d chunks", len(chunkTimes))
		}
	}
	if len(chunkTimes) < 2 {
		t.Fatalf("expected multiple chunks across time, got %d", len(chunkTimes))
	}
	// At least one chunk should arrive >200ms after the first — i.e. they're
	// truly streamed, not delivered as a single end-of-command blob.
	gap := chunkTimes[len(chunkTimes)-1].Sub(chunkTimes[0])
	if gap < 200*time.Millisecond {
		t.Fatalf("chunks delivered too close together (%s); not streaming", gap)
	}
}
```

- [ ] **Step 4.2: Run integration tests**

```bash
go test -race -v -run Integration ./internal/shell/
```

Expected: all 7 tests PASS. If on macOS, you may need to install GNU bash via brew (`brew install bash`) — the bundled `/bin/bash` is ancient but should still work.

- [ ] **Step 4.3: Commit**

```bash
git add internal/shell/shell_integration_test.go
git commit -m "test(shell): integration tests against real bash"
```

---

## Task 5: Store — Record type and atomic Save/Get

**Files:**
- Create: `internal/store/record.go`
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`

- [ ] **Step 5.1: Write the failing tests**

Create `internal/store/store_test.go`:
```go
package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return s
}

func TestStore_SaveAndGet(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := Record{
		ID:        "01HAB",
		Command:   "ls",
		Cwd:       "/tmp",
		StartedAt: now,
		Status:    StatusRunning,
	}
	if err := s.Save(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Get("01HAB")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Command != "ls" || got.Status != StatusRunning {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_SaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	rec := Record{ID: "A", Command: "x", Status: StatusRunning}
	if err := s.Save(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	// No .tmp files should remain after a successful save.
	entries, _ := os.ReadDir(filepath.Join(s.Dir(), "commands"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("missing")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_ListReturnsMostRecentFirst(t *testing.T) {
	s := newTestStore(t)
	for i, id := range []string{"A", "B", "C"} {
		rec := Record{
			ID:        id,
			Command:   id,
			Status:    StatusCompleted,
			StartedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := s.Save(rec); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		time.Sleep(10 * time.Millisecond) // ensure mtime ordering
	}
	list, err := s.List(10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	if list[0].ID != "C" || list[2].ID != "A" {
		t.Fatalf("order wrong: %+v", list)
	}
}

func TestStore_ListRespectsBefore(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"A", "B", "C", "D"} {
		_ = s.Save(Record{ID: id, Command: id, Status: StatusCompleted, StartedAt: time.Now().UTC()})
		time.Sleep(10 * time.Millisecond)
	}
	list, err := s.List(10, "C")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// "before=C" → returns records strictly older than C (A, B).
	gotIDs := make([]string, len(list))
	for i, r := range list {
		gotIDs[i] = r.ID
	}
	if len(list) != 2 || list[0].ID != "B" || list[1].ID != "A" {
		t.Fatalf("got %v, want [B A]", gotIDs)
	}
}

func TestStore_AppendOutputAndOutputPath(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(Record{ID: "X", Command: "x", Status: StatusRunning})
	if err := s.WriteOutput("X", []byte("hello\nworld\n")); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	rec, _ := s.Get("X")
	if rec.OutputPath == "" {
		t.Fatalf("OutputPath empty after WriteOutput")
	}
	data, err := os.ReadFile(rec.OutputPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(data) != "hello\nworld\n" {
		t.Fatalf("got %q", data)
	}
}

func TestStore_SweepMarksRunningAsInterrupted(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(Record{ID: "stuck", Status: StatusRunning, Command: "sleep"})
	_ = s.Save(Record{ID: "done", Status: StatusCompleted, Command: "ls"})
	if err := s.SweepRunningToInterrupted(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stuck, _ := s.Get("stuck")
	if stuck.Status != StatusInterrupted {
		t.Fatalf("stuck status = %s, want interrupted", stuck.Status)
	}
	done, _ := s.Get("done")
	if done.Status != StatusCompleted {
		t.Fatalf("done changed unexpectedly: %s", done.Status)
	}
}
```

- [ ] **Step 5.2: Run tests to verify they fail**

```bash
go test -race ./internal/store/
```

Expected: fail (package doesn't exist yet).

- [ ] **Step 5.3: Implement record.go**

Create `internal/store/record.go`:
```go
package store

import "time"

type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusInterrupted Status = "interrupted"
	StatusStopped     Status = "stopped"
)

type Record struct {
	ID              string    `json:"id"`
	Command         string    `json:"command"`
	Cwd             string    `json:"cwd"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	OutputPath      string    `json:"output_path,omitempty"`
	OutputTruncated bool      `json:"output_truncated"`
	Status          Status    `json:"status"`
}
```

- [ ] **Step 5.4: Implement store.go**

Create `internal/store/store.go`:
```go
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

var ErrNotFound = errors.New("record not found")

type Store struct {
	dir string
}

func New(dir string) (*Store, error) {
	for _, sub := range []string{"commands", "outputs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) commandPath(id string) string {
	return filepath.Join(s.dir, "commands", id+".json")
}

func (s *Store) outputPath(id string) string {
	return filepath.Join(s.dir, "outputs", id+".log")
}

// Save writes or overwrites the metadata file atomically (tmp + rename).
func (s *Store) Save(r Record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	final := s.commandPath(r.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (s *Store) Get(id string) (Record, error) {
	data, err := os.ReadFile(s.commandPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// WriteOutput writes the entire output buffer for a command to its log file,
// then updates the record's OutputPath.
func (s *Store) WriteOutput(id string, body []byte) error {
	path := s.outputPath(id)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	r, err := s.Get(id)
	if err != nil {
		return err
	}
	r.OutputPath = path
	return s.Save(r)
}

// ReadOutput reads the output file for a command, or returns empty if none.
func (s *Store) ReadOutput(id string) ([]byte, error) {
	r, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if r.OutputPath == "" {
		return nil, nil
	}
	return os.ReadFile(r.OutputPath)
}

// List returns records sorted by StartedAt descending. If before != "", only
// records strictly older than the one with that ID are returned.
func (s *Store) List(limit int, before string) ([]Record, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "commands"))
	if err != nil {
		return nil, err
	}
	var all []Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		r, err := s.Get(id)
		if err != nil {
			continue
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartedAt.After(all[j].StartedAt)
	})
	if before != "" {
		var beforeRec *Record
		for i := range all {
			if all[i].ID == before {
				beforeRec = &all[i]
				break
			}
		}
		if beforeRec != nil {
			filtered := all[:0]
			for _, r := range all {
				if r.StartedAt.Before(beforeRec.StartedAt) {
					filtered = append(filtered, r)
				}
			}
			all = filtered
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// SweepRunningToInterrupted is called once at boot. Any record left in the
// "running" state from a previous process belongs to a bash that no longer
// exists, so it gets marked interrupted.
func (s *Store) SweepRunningToInterrupted() error {
	all, err := s.List(0, "")
	if err != nil {
		return err
	}
	for _, r := range all {
		if r.Status == StatusRunning {
			r.Status = StatusInterrupted
			if err := s.Save(r); err != nil {
				return fmt.Errorf("sweep %s: %w", r.ID, err)
			}
		}
	}
	return nil
}
```

- [ ] **Step 5.5: Run tests to verify they pass**

```bash
go test -race ./internal/store/
```

Expected: all 7 tests PASS.

- [ ] **Step 5.6: Commit**

```bash
git add internal/store/
git commit -m "feat(store): JSON file persistence with atomic writes and sweep"
```

---

## Task 6: Auth — credential check and rate limit

**Files:**
- Create: `internal/auth/auth.go`
- Create: `internal/auth/auth_test.go`
- Create: `internal/auth/ratelimit.go`
- Create: `internal/auth/ratelimit_test.go`

- [ ] **Step 6.1: Write the failing tests**

Create `internal/auth/auth_test.go`:
```go
package auth

import "testing"

func TestCheckLogin_CorrectCredentials(t *testing.T) {
	a := Auth{User: "admin", Password: "s3cret", Token: "TOK"}
	tok, ok := a.CheckLogin("admin", "s3cret")
	if !ok {
		t.Fatal("want ok")
	}
	if tok != "TOK" {
		t.Fatalf("token = %q, want TOK", tok)
	}
}

func TestCheckLogin_WrongPassword(t *testing.T) {
	a := Auth{User: "admin", Password: "s3cret", Token: "TOK"}
	if _, ok := a.CheckLogin("admin", "wrong"); ok {
		t.Fatal("want !ok for wrong password")
	}
}

func TestCheckLogin_WrongUser(t *testing.T) {
	a := Auth{User: "admin", Password: "s3cret", Token: "TOK"}
	if _, ok := a.CheckLogin("guest", "s3cret"); ok {
		t.Fatal("want !ok for wrong user")
	}
}

func TestVerifyToken(t *testing.T) {
	a := Auth{Token: "TOK"}
	if !a.VerifyToken("TOK") {
		t.Fatal("want valid")
	}
	if a.VerifyToken("WRONG") {
		t.Fatal("want invalid")
	}
	if a.VerifyToken("") {
		t.Fatal("want empty rejected")
	}
}
```

Create `internal/auth/ratelimit_test.go`:
```go
package auth

import (
	"testing"
	"time"
)

func TestRateLimiter_AllowsUnderQuota(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.Allow("ip1") {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
}

func TestRateLimiter_BlocksOverQuota(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	_ = rl.Allow("ip1")
	_ = rl.Allow("ip1")
	if rl.Allow("ip1") {
		t.Fatal("3rd attempt should be blocked")
	}
}

func TestRateLimiter_PerIPSeparate(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	if !rl.Allow("ip1") {
		t.Fatal("ip1 first allowed")
	}
	if !rl.Allow("ip2") {
		t.Fatal("ip2 first allowed")
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)
	_ = rl.Allow("ip1")
	if rl.Allow("ip1") {
		t.Fatal("second should be blocked")
	}
	time.Sleep(80 * time.Millisecond)
	if !rl.Allow("ip1") {
		t.Fatal("after refill, should be allowed")
	}
}
```

- [ ] **Step 6.2: Run tests to verify they fail**

```bash
go test -race ./internal/auth/
```

Expected: fail (package doesn't exist yet).

- [ ] **Step 6.3: Implement auth.go**

Create `internal/auth/auth.go`:
```go
package auth

import (
	"crypto/subtle"
	"errors"
	"os"
)

// Auth holds the static credentials loaded from environment variables.
type Auth struct {
	User     string
	Password string
	Token    string
}

// FromEnv constructs Auth from ALFRED_USER, ALFRED_PASSWORD, ALFRED_TOKEN.
// All three are required and must be non-empty.
func FromEnv() (Auth, error) {
	a := Auth{
		User:     os.Getenv("ALFRED_USER"),
		Password: os.Getenv("ALFRED_PASSWORD"),
		Token:    os.Getenv("ALFRED_TOKEN"),
	}
	if a.User == "" || a.Password == "" || a.Token == "" {
		return Auth{}, errors.New("ALFRED_USER, ALFRED_PASSWORD, ALFRED_TOKEN must all be set")
	}
	return a, nil
}

// CheckLogin returns the token if user+password match, else "", false.
// Uses constant-time comparison to defeat timing oracles.
func (a Auth) CheckLogin(user, password string) (string, bool) {
	uMatch := subtle.ConstantTimeCompare([]byte(user), []byte(a.User)) == 1
	pMatch := subtle.ConstantTimeCompare([]byte(password), []byte(a.Password)) == 1
	if uMatch && pMatch {
		return a.Token, true
	}
	return "", false
}

// VerifyToken returns true iff token equals the configured token.
// Empty token always fails.
func (a Auth) VerifyToken(token string) bool {
	if token == "" || a.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(a.Token)) == 1
}
```

- [ ] **Step 6.4: Implement ratelimit.go**

Create `internal/auth/ratelimit.go`:
```go
package auth

import (
	"sync"
	"time"
)

// RateLimiter is a simple per-key token bucket: each key gets `capacity`
// permits per `window`, refilled smoothly.
type RateLimiter struct {
	capacity int
	window   time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewRateLimiter(capacity int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		capacity: capacity,
		window:   window,
		buckets:  make(map[string]*bucket),
	}
}

// Allow consumes one token for the key. Returns true if allowed.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(r.capacity), last: now}
		r.buckets[key] = b
	}
	// Refill.
	elapsed := now.Sub(b.last).Seconds()
	refill := elapsed * float64(r.capacity) / r.window.Seconds()
	b.tokens += refill
	if b.tokens > float64(r.capacity) {
		b.tokens = float64(r.capacity)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}
```

- [ ] **Step 6.5: Run tests to verify they pass**

```bash
go test -race ./internal/auth/
```

Expected: all 7 tests PASS.

- [ ] **Step 6.6: Commit**

```bash
git add internal/auth/
git commit -m "feat(auth): static credentials, token check, per-IP rate limit"
```

---

## Task 7: Tidy and final verification

- [ ] **Step 7.1: Tidy modules**

```bash
go mod tidy
```

- [ ] **Step 7.2: Run all tests**

```bash
make test
```

Expected: all unit + integration tests pass. Output looks roughly like:
```
ok  	github.com/jesseliu/headless-alfred/internal/auth     0.05s
ok  	github.com/jesseliu/headless-alfred/internal/shell    1.50s
ok  	github.com/jesseliu/headless-alfred/internal/store    0.04s
```

- [ ] **Step 7.3: Commit any tidy changes**

```bash
git add go.mod go.sum
git commit -m "chore: go mod tidy" --allow-empty
```

(Use `--allow-empty` only if there's nothing to commit; otherwise just commit normally.)

---

## Self-Review Notes

This plan covers the following spec sections fully:
- §3.1 sentinel framing ✓ (Task 2)
- §4 module boundaries for `shell`, `store`, `auth` ✓ (Tasks 1-6)
- §5 broadcaster fan-out + invariants on disconnect ✓ (Task 1, Task 3)
- §7 file formats + atomicity + sweep ✓ (Task 5)
- §8 auth with constant-time compare ✓ (Task 6)
- §10 rate limit per IP ✓ (Task 6)

What's deferred to Plan 2:
- HTTP routing (`internal/api/`)
- WebSocket hub (consumes `Shell.SubscribeEvents`)
- Static file serving (`internal/static/`)
- `cmd/alfred-server/main.go` wiring

What's deferred to Plan 3+:
- Frontend
- Docker image
- K8s manifests
- E2E
