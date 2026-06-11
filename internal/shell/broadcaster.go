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

// subState tracks per-subscriber overflow state.
type subState struct {
	inDrop      bool // currently in drop episode
	dropPending bool // drop marker not yet delivered to channel
}

// Broadcaster delivers each Publish to every active Subscriber.
// A Subscriber whose channel is full receives a single Drop marker instead
// of the chunk; the publisher never blocks on a slow subscriber.
type Broadcaster struct {
	mu   sync.RWMutex
	subs map[*Subscriber]*subState
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[*Subscriber]*subState)}
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
	b.subs[s] = &subState{}
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
	for sub, st := range b.subs {
		if st.inDrop {
			// Try to deliver any pending drop marker first.
			if st.dropPending {
				select {
				case sub.C <- Message{Drop: true}:
					st.dropPending = false
				default:
					// Channel still full; drop marker stays pending.
					continue
				}
			}
			// Drop marker delivered. Wait until channel drains before
			// resuming normal sends (so subscriber can process the marker).
			if len(sub.C) == 0 {
				st.inDrop = false
				select {
				case sub.C <- Message{Bytes: cp}:
				default:
					// Immediately full again; start a new drop episode.
					st.inDrop = true
					st.dropPending = true
				}
			}
			continue
		}
		select {
		case sub.C <- Message{Bytes: cp}:
		default:
			// Buffer full — enter drop episode. Evict the oldest message to
			// make room for the drop marker so it is always delivered.
			select {
			case <-sub.C: // discard one buffered chunk to make room
			default:
			}
			select {
			case sub.C <- Message{Drop: true}:
				st.inDrop = true
				st.dropPending = false
			default:
				// Shouldn't happen after eviction, but be safe.
				st.inDrop = true
				st.dropPending = true
			}
		}
	}
}
