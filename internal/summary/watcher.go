package summary

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher tails <dataDir>/summaries/ and invokes onWrite(sid)
// whenever a `<sid>.md` file is created or modified, with a
// per-file debounce so a single Write that produces multiple
// fsnotify events still fires the callback once.
type Watcher struct {
	w       *fsnotify.Watcher
	onWrite func(sessionID string)
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	pending map[string]*time.Timer
}

// StartWatcher creates the summaries directory if missing, starts
// an fsnotify watcher on it, and dispatches debounced filename
// events to onWrite in a background goroutine.
//
// Errors during mkdir or fsnotify init are returned; on success
// the caller must call Stop() to release the watcher. The watcher
// is intentionally fail-stop on initial errors so main.go can log
// + continue without it (sidebar becomes stale but the app still
// works).
func StartWatcher(dataDir string, onWrite func(sessionID string)) (*Watcher, error) {
	if onWrite == nil {
		return nil, errors.New("summary.StartWatcher: onWrite required")
	}
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(dir); err != nil {
		fw.Close()
		return nil, err
	}
	w := &Watcher{
		w:       fw,
		onWrite: onWrite,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		pending: map[string]*time.Timer{},
	}
	go w.loop()
	return w, nil
}

// Stop blocks until the watcher goroutine drains AND any pending
// debounced callbacks are cancelled. Safe to call from a defer.
// After Stop returns, the onWrite callback is guaranteed NOT to
// fire again, even if a debounce timer was armed.
func (w *Watcher) Stop() {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	for _, t := range w.pending {
		t.Stop()
	}
	w.pending = map[string]*time.Timer{}
	w.mu.Unlock()
	w.w.Close()
}

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			// We care about Create + Write; Rename/Remove are
			// frontend-visible too but the next Read will 404
			// and the UI re-renders the empty state, no push
			// needed.
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			sid, ok := parseSummaryFilename(filepath.Base(ev.Name))
			if !ok {
				continue
			}
			w.schedule(sid)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn("summary.Watcher: fsnotify error", "err", err)
		}
	}
}

const debounce = 200 * time.Millisecond

// schedule arms a per-sid timer; bursts of events for the same
// file coalesce into a single onWrite call.
func (w *Watcher) schedule(sid string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[sid]; ok {
		t.Reset(debounce)
		return
	}
	w.pending[sid] = time.AfterFunc(debounce, func() {
		w.mu.Lock()
		delete(w.pending, sid)
		w.mu.Unlock()
		w.onWrite(sid)
	})
}

// parseSummaryFilename returns the session id from a filename of
// the form "<sid>.md". Returns ("", false) for anything else.
// Used to ignore stray *.txt, dotfiles, etc.
func parseSummaryFilename(name string) (string, bool) {
	if strings.HasPrefix(name, ".") {
		return "", false
	}
	if !strings.HasSuffix(name, ".md") {
		return "", false
	}
	sid := strings.TrimSuffix(name, ".md")
	if sid == "" {
		return "", false
	}
	return sid, true
}
