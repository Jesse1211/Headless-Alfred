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

func (f *FakeRunner) SendText(session, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SendText", session, text)
	return f.takeErr("SendText")
}

func (f *FakeRunner) SendEnter(session string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SendEnter", session)
	return f.takeErr("SendEnter")
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
