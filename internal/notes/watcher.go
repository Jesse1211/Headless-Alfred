package notes

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

// Watcher tails <dataDir>/notes/ and invokes onWrite(sid) whenever
// a <sid>.md file is created or modified. Per-file 200ms debounce.
type Watcher struct {
	w       *fsnotify.Watcher
	onWrite func(sessionID string)
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	pending map[string]*time.Timer
}

const debounce = 200 * time.Millisecond

func StartWatcher(dataDir string, onWrite func(sessionID string)) (*Watcher, error) {
	if onWrite == nil {
		return nil, errors.New("notes.StartWatcher: onWrite required")
	}
	dir := Dir(dataDir)
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
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			sid, ok := parseNotesFilename(filepath.Base(ev.Name))
			if !ok {
				continue
			}
			w.schedule(sid)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn("notes watcher", "err", err)
		}
	}
}

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

// parseNotesFilename returns (sid, true) for <sid>.md, skipping
// dotfiles and non-md.
func parseNotesFilename(name string) (string, bool) {
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
