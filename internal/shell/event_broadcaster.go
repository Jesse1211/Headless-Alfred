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
