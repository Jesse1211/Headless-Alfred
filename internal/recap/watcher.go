package recap

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	w       *fsnotify.Watcher
	onWrite func(date string)
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	pending map[string]*time.Timer
}

const debounce = 200 * time.Millisecond

var recapFilename = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2})\.md$`)

func StartWatcher(dataDir string, onWrite func(date string)) (*Watcher, error) {
	if onWrite == nil {
		return nil, errors.New("recap.StartWatcher: onWrite required")
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

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			date := parseRecapFilename(filepath.Base(ev.Name))
			if date == "" {
				continue
			}
			w.schedule(date)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn("recap watcher", "err", err)
		case <-w.stop:
			return
		}
	}
}

func (w *Watcher) schedule(date string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[date]; ok {
		t.Reset(debounce)
		return
	}
	w.pending[date] = time.AfterFunc(debounce, func() {
		w.mu.Lock()
		delete(w.pending, date)
		w.mu.Unlock()
		w.onWrite(date)
	})
}

func (w *Watcher) Stop() {
	close(w.stop)
	_ = w.w.Close()
	<-w.done
	w.mu.Lock()
	timers := w.pending
	w.pending = map[string]*time.Timer{}
	w.mu.Unlock()
	for _, t := range timers {
		t.Stop()
	}
}

func parseRecapFilename(name string) string {
	m := recapFilename.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return m[1]
}
