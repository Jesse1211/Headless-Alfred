package claudestate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// snapshotFile is the on-disk envelope for claude.json. The schema
// field set follows the spec: version + identity (sessionId,
// claudeUuid) + audit (writtenAt) + the persistable turns slice.
// Everything else in ClaudeState is recomputed on load.
type snapshotFile struct {
	Version    int          `json:"version"`
	SessionID  string       `json:"sessionId"`
	ClaudeUUID string       `json:"claudeUuid"`
	WrittenAt  time.Time    `json:"writtenAt"`
	Turns      []ClaudeTurn `json:"turns"`
}

const snapshotVersion = 1

// Persister owns one snapshot.json file on behalf of a SessionState.
// Run as a single goroutine: MarkDirty signals a write should happen
// after the debounce window; Flush forces an immediate write and
// blocks until it lands. Holding a file-level flock prevents two
// server processes from racing on the same snapshot.
type Persister struct {
	path     string
	tmpPath  string
	state    *SessionState
	debounce time.Duration

	dirty    chan struct{}   // cap 1 — coalesces signals
	flushReq chan chan error // synchronous flush
	closeReq chan chan error // synchronous close

	flockFile *os.File
	stopped   sync.Once
}

// NewPersister allocates the goroutine resources and acquires the
// flock. Caller MUST call Run to start servicing signals.
func NewPersister(path string, state *SessionState, debounce time.Duration) (*Persister, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("claudestate: mkdir for snapshot: %w", err)
	}
	lockPath := path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("claudestate: open lockfile: %w", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		return nil, fmt.Errorf("claudestate: snapshot held by another process: %w", err)
	}
	// Best-effort orphan tmp cleanup from a previous crash.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		_ = os.Remove(path + ".tmp")
	}
	return &Persister{
		path:      path,
		tmpPath:   path + ".tmp",
		state:     state,
		debounce:  debounce,
		dirty:     make(chan struct{}, 1),
		flushReq:  make(chan chan error),
		closeReq:  make(chan chan error),
		flockFile: lf,
	}, nil
}

// Run is the goroutine main loop. Exits when Close is called or ctx
// is canceled.
func (p *Persister) Run(ctx context.Context) {
	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}
	defer stopTimer()
	for {
		select {
		case <-p.dirty:
			stopTimer()
			timer = time.NewTimer(p.debounce)
		case <-timerC(timer):
			timer = nil
			if err := p.writeSnapshot(); err != nil {
				slog.Error("claudestate: snapshot write failed",
					"sessionID", p.state.SessionID(), "err", err)
			}
		case ack := <-p.flushReq:
			stopTimer()
			ack <- p.writeSnapshot()
		case ack := <-p.closeReq:
			stopTimer()
			err := p.writeSnapshot()
			p.releaseLock()
			ack <- err
			return
		case <-ctx.Done():
			stopTimer()
			p.releaseLock()
			return
		}
	}
}

// MarkDirty signals the goroutine that state changed. Non-blocking:
// if a signal is already queued the second one collapses into it.
func (p *Persister) MarkDirty() {
	select {
	case p.dirty <- struct{}{}:
	default:
	}
}

// Flush blocks until the next snapshot write completes (or fails).
func (p *Persister) Flush(ctx context.Context) error {
	ack := make(chan error, 1)
	select {
	case p.flushReq <- ack:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close synchronously flushes one final time and shuts down the
// goroutine. Idempotent.
func (p *Persister) Close(ctx context.Context) error {
	var finalErr error
	p.stopped.Do(func() {
		ack := make(chan error, 1)
		select {
		case p.closeReq <- ack:
			select {
			case finalErr = <-ack:
			case <-ctx.Done():
				finalErr = ctx.Err()
			}
		case <-ctx.Done():
			finalErr = ctx.Err()
		}
	})
	return finalErr
}

// writeSnapshot performs the standard tmp → fsync → rename atomic
// write. Hand-rolled because os.WriteFile skips Sync.
func (p *Persister) writeSnapshot() error {
	// Capture state under read lock.
	var snap snapshotFile
	p.state.View(func(st *ClaudeState) {
		copied := st.DeepCopy()
		snap = snapshotFile{
			Version:    snapshotVersion,
			SessionID:  p.state.SessionID(),
			ClaudeUUID: p.state.ClaudeUUID(),
			WrittenAt:  time.Now().UTC(),
			Turns:      copied.Turns,
		}
	})

	// Marshal outside the lock.
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	f, err := os.OpenFile(p.tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(p.tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(p.tmpPath)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p.tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(p.tmpPath, p.path); err != nil {
		os.Remove(p.tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	// fsync parent dir for crash durability.
	if d, err := os.Open(filepath.Dir(p.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (p *Persister) releaseLock() {
	if p.flockFile != nil {
		_ = syscall.Flock(int(p.flockFile.Fd()), syscall.LOCK_UN)
		_ = p.flockFile.Close()
		p.flockFile = nil
	}
}

// timerC returns the channel of a possibly-nil timer (nil channel
// blocks forever in a select, which is the behavior we want when
// nothing is scheduled).
func timerC(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}
