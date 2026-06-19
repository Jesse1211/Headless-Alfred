package api

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/jesseliu/headless-alfred/internal/claudebgtasks"
)

// bgTaskLogSub holds the live watcher + goroutine-exit channel for one
// per-WS subscription to a bg-task log file.
type bgTaskLogSub struct {
	watcher *fsnotify.Watcher
	done    chan struct{} // closed when the goroutine exits
}

// bgTaskLogSubs is a per-WS-connection map from taskID to active subscription.
// All public methods are safe for concurrent use.
type bgTaskLogSubs struct {
	mu   sync.Mutex
	subs map[string]*bgTaskLogSub
}

func newBgTaskLogSubs() *bgTaskLogSubs {
	return &bgTaskLogSubs{subs: make(map[string]*bgTaskLogSub)}
}

// set registers a subscription. Any existing subscription for the same taskID
// is returned so the caller can close it before replacing.
func (s *bgTaskLogSubs) set(taskID string, sub *bgTaskLogSub) (prev *bgTaskLogSub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev = s.subs[taskID]
	s.subs[taskID] = sub
	return prev
}

// get returns the subscription for taskID, or nil if none.
func (s *bgTaskLogSubs) get(taskID string) *bgTaskLogSub {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subs[taskID]
}

// delete removes and returns the subscription for taskID, or nil.
func (s *bgTaskLogSubs) delete(taskID string) *bgTaskLogSub {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subs[taskID]
	delete(s.subs, taskID)
	return sub
}

// closeAll closes every registered subscription. Called on WS disconnect.
func (s *bgTaskLogSubs) closeAll() {
	s.mu.Lock()
	all := make(map[string]*bgTaskLogSub, len(s.subs))
	for k, v := range s.subs {
		all[k] = v
	}
	s.subs = make(map[string]*bgTaskLogSub)
	s.mu.Unlock()

	for _, sub := range all {
		_ = sub.watcher.Close()
		<-sub.done
	}
}

// emitWatcherUnavailable sends a bg_task_stdout_chunk frame indicating
// that the server could not establish an fsnotify watcher for taskID.
func emitWatcherUnavailable(taskID string, write func(OutMsg) error) {
	_ = write(OutMsg{
		Type: TypeBgTaskStdoutChunk,
		Payload: BgTaskStdoutChunkPayload{
			TaskID: taskID,
			Status: "watcher_unavailable",
		},
	})
}

// handleSubscribeBgTaskLog processes a subscribe_bg_task_log inbound frame.
//
// It:
//  1. Validates the taskID against taskIDRe (defined in bg_task_log_handler.go).
//  2. Resolves the output file path via MetaResolver + CWDResolver.
//  3. Opens an fsnotify watcher on the file.
//  4. Seeks to the end of the file so only NEW bytes are streamed.
//  5. Spawns a goroutine that emits bg_task_stdout_chunk frames on each
//     fsnotify.Write event.
//
// If the watcher cannot be created or the file watched, a single
// bg_task_stdout_chunk{Status:"watcher_unavailable"} frame is emitted and
// the function returns without spawning a goroutine.
//
// ctx should be derived from the WS connection lifetime; cancelling it stops
// the goroutine without an explicit unsubscribe frame.
func handleSubscribeBgTaskLog(
	ctx context.Context,
	payload SubscribeBgTaskLogPayload,
	subs *bgTaskLogSubs,
	write func(OutMsg) error,
	sessionID string,
	meta MetaResolver,
	cwdRes CWDResolver,
) {
	taskID := payload.TaskID
	if !taskIDRe.MatchString(taskID) {
		slog.Warn("subscribe_bg_task_log: invalid taskID", "taskId", taskID)
		return
	}

	// Resolve Claude UUID for the session.
	sessionUUID, err := meta.ClaudeUUIDFor(sessionID)
	if err != nil {
		slog.Warn("subscribe_bg_task_log: meta resolve", "sid", sessionID, "err", err)
		emitWatcherUnavailable(taskID, write)
		return
	}
	if sessionUUID == "" {
		slog.Warn("subscribe_bg_task_log: no claude UUID for session", "sid", sessionID)
		emitWatcherUnavailable(taskID, write)
		return
	}

	// Resolve CWD.
	cwd, err := cwdRes.CWDFor(sessionID)
	if err != nil || cwd == "" {
		slog.Warn("subscribe_bg_task_log: cwd resolve", "sid", sessionID, "err", err)
		emitWatcherUnavailable(taskID, write)
		return
	}

	logPath := claudebgtasks.OutputPath(cwd, sessionUUID, taskID)

	// Create the fsnotify watcher.
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("subscribe_bg_task_log: fsnotify.NewWatcher", "err", err)
		emitWatcherUnavailable(taskID, write)
		return
	}

	// Add the specific file to the watcher. This fails if the file doesn't
	// exist yet, which triggers the "watcher_unavailable" degradation path
	// (ADR-014 OQ-06 fallback).
	if err := fw.Add(logPath); err != nil {
		slog.Warn("subscribe_bg_task_log: watcher.Add", "path", logPath, "err", err)
		_ = fw.Close()
		emitWatcherUnavailable(taskID, write)
		return
	}

	// Open the file and seek to the end so we only deliver NEW bytes.
	f, err := os.Open(logPath)
	if err != nil {
		slog.Warn("subscribe_bg_task_log: open", "path", logPath, "err", err)
		_ = fw.Close()
		emitWatcherUnavailable(taskID, write)
		return
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		slog.Warn("subscribe_bg_task_log: seek", "path", logPath, "err", err)
		_ = f.Close()
		_ = fw.Close()
		emitWatcherUnavailable(taskID, write)
		return
	}

	done := make(chan struct{})
	sub := &bgTaskLogSub{watcher: fw, done: done}

	// Replace any existing subscription for the same taskID.
	if prev := subs.set(taskID, sub); prev != nil {
		_ = prev.watcher.Close()
		<-prev.done
	}

	// Goroutine: forward fsnotify.Write events to the WS client.
	go func() {
		defer close(done)
		defer f.Close()
		// Do not defer fw.Close() here — the watcher is owned by the sub
		// and closed either by handleUnsubscribeBgTaskLog or closeAll.
		// Closing it from here would race with the caller holding the ref.
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-fw.Events:
				if !ok {
					// Watcher was closed externally (unsubscribe / disconnect).
					return
				}
				if ev.Op&fsnotify.Write == 0 {
					continue
				}
				// Read from current position to EOF.
				chunk, err := io.ReadAll(f)
				if err != nil {
					slog.Warn("subscribe_bg_task_log: read", "path", logPath, "err", err)
					continue
				}
				if len(chunk) == 0 {
					continue
				}
				_ = write(OutMsg{
					Type: TypeBgTaskStdoutChunk,
					Payload: BgTaskStdoutChunkPayload{
						TaskID: taskID,
						Bytes:  base64.StdEncoding.EncodeToString(chunk),
					},
				})
			case err, ok := <-fw.Errors:
				if !ok {
					return
				}
				slog.Warn("subscribe_bg_task_log: watcher error", "taskId", taskID, "err", err)
			}
		}
	}()
}

// handleUnsubscribeBgTaskLog processes an unsubscribe_bg_task_log frame.
// It closes the watcher for the given taskID and returns the subscription's
// done channel so callers can wait for the goroutine to exit (useful in tests).
// Returns a closed channel if no subscription was active (safe to read from).
func handleUnsubscribeBgTaskLog(payload UnsubscribeBgTaskLogPayload, subs *bgTaskLogSubs) <-chan struct{} {
	taskID := payload.TaskID
	sub := subs.delete(taskID)
	if sub == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	_ = sub.watcher.Close()
	return sub.done
}
