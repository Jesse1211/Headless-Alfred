// Package diskwatcher is a tiny generic wrapper around fsnotify that
// adds per-key debounce and graceful drain semantics. Used by the
// per-session summary/notes watchers and the per-date recap watcher
// — all three were near-identical before this extraction.
//
// The key type K is what the caller wants to identify "which thing
// changed" — usually a session ID or a date string. The parse
// callback turns a filename into a K (or rejects the file with
// false). The onWrite callback fires AT MOST ONCE per K per debounce
// window, on a background goroutine.
package diskwatcher

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher tails one directory and fires onWrite(key) for each
// Create/Write event whose filename parses to a key, debounced.
type Watcher[K comparable] struct {
	w       *fsnotify.Watcher
	onWrite func(K)
	parse   func(string) (K, bool)
	logName string

	stop chan struct{}
	done chan struct{}

	mu       sync.Mutex
	pending  map[K]*time.Timer
	debounce time.Duration
}

// Start creates the directory if missing, opens an fsnotify watcher
// on it, and dispatches debounced filename events to onWrite from a
// background goroutine.
//
// parse returns (key, true) for filenames the caller cares about,
// (zero, false) to skip (dotfiles, wrong extension, malformed names).
//
// debounce is the per-key trailing-edge delay. 200ms is the value
// every existing call site uses.
//
// logName is the source identifier used in slog warnings ("summary
// watcher", "notes watcher", "recap watcher"); kept distinct so the
// pre-extraction log lines remain identifiable.
//
// Errors during mkdir or fsnotify init are returned — the caller
// is expected to log + carry on without the watcher rather than
// abort boot (the UI degrades to stale data, the rest of the app
// keeps working).
func Start[K comparable](
	dir string,
	parse func(filename string) (K, bool),
	debounce time.Duration,
	onWrite func(K),
	logName string,
) (*Watcher[K], error) {
	if onWrite == nil {
		return nil, errors.New("diskwatcher.Start: onWrite required")
	}
	if parse == nil {
		return nil, errors.New("diskwatcher.Start: parse required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(dir); err != nil {
		_ = fw.Close()
		return nil, err
	}
	w := &Watcher[K]{
		w:        fw,
		onWrite:  onWrite,
		parse:    parse,
		logName:  logName,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		pending:  map[K]*time.Timer{},
		debounce: debounce,
	}
	go w.loop()
	return w, nil
}

// Stop blocks until the loop goroutine drains AND pending debounce
// timers are cancelled. After Stop returns, the onWrite callback is
// guaranteed NOT to fire again.
//
// Order matters: signal stop, wait for the loop to exit, then close
// fsnotify. Closing fsnotify first would race with the loop's read
// of w.w.Events and could lose the last in-flight events.
func (w *Watcher[K]) Stop() {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	for _, t := range w.pending {
		t.Stop()
	}
	w.pending = map[K]*time.Timer{}
	w.mu.Unlock()
	_ = w.w.Close()
}

func (w *Watcher[K]) loop() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			key, ok := w.parse(filepath.Base(ev.Name))
			if !ok {
				continue
			}
			w.schedule(key)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn(w.logName, "err", err)
		}
	}
}

func (w *Watcher[K]) schedule(key K) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[key]; ok {
		t.Reset(w.debounce)
		return
	}
	w.pending[key] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.pending, key)
		w.mu.Unlock()
		w.onWrite(key)
	})
}
