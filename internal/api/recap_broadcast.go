package api

import (
	"sync"
)

// recapBroadcaster fans out a single source channel of recap dates
// to every subscriber. Each WS client subscribes on connect,
// unsubscribes on disconnect. The source channel is process-wide
// (one fsnotify watcher serves all clients).
type recapBroadcaster struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newRecapBroadcaster(source <-chan string) *recapBroadcaster {
	b := &recapBroadcaster{subs: map[chan string]struct{}{}}
	if source == nil {
		return b
	}
	go func() {
		for date := range source {
			b.mu.Lock()
			for ch := range b.subs {
				select {
				case ch <- date:
				default:
					// subscriber's queue full — drop. UI will be
					// momentarily stale but a manual refresh fixes.
				}
			}
			b.mu.Unlock()
		}
	}()
	return b
}

func (b *recapBroadcaster) subscribe() (chan string, func()) {
	ch := make(chan string, 8)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		// Drain + close. Subscribers must not panic-on-closed.
		close(ch)
	}
}
