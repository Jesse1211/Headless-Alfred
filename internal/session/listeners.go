package session

import "sync"

// listenerSet is a multi-subscriber callback registry. Add registers a
// callback and returns a remove function; Fire snapshots the current
// registrants under the lock then invokes them OUTSIDE the lock so a
// callback can re-enter Manager methods without deadlocking. Nil
// entries are tolerated (left behind by remove() and skipped on Fire),
// matching the pre-generic Manager behavior that earlier tabs would
// keep working after a later tab unsubscribes.
type listenerSet[T any] struct {
	mu  sync.Mutex
	fns []func(T)
}

// Add appends fn and returns a remove function. remove is idempotent
// and safe to call after the listenerSet itself is gone.
func (l *listenerSet[T]) Add(fn func(T)) func() {
	l.mu.Lock()
	l.fns = append(l.fns, fn)
	idx := len(l.fns) - 1
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if idx < len(l.fns) {
			l.fns[idx] = nil
		}
	}
}

// Fire snapshots the current non-nil callbacks then invokes them all
// outside the lock with arg.
func (l *listenerSet[T]) Fire(arg T) {
	l.mu.Lock()
	snap := make([]func(T), 0, len(l.fns))
	for _, fn := range l.fns {
		if fn != nil {
			snap = append(snap, fn)
		}
	}
	l.mu.Unlock()
	for _, fn := range snap {
		fn(arg)
	}
}

// renameEvent carries the arguments for a rename notification.
type renameEvent struct {
	SessionID string
	NewName   string
}
