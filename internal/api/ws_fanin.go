package api

import (
	"reflect"

	"github.com/jesseliu/headless-alfred/internal/shell"
)

// FanInEvent is one delivery: which session it came from, and the event.
type FanInEvent struct {
	SessionID string
	Event     shell.CommandEvent
}

// NamedSubscriber pairs a sessionID with the subscriber that delivers
// its events.
type NamedSubscriber struct {
	SessionID string
	Sub       *shell.EventSubscriber
}

// FanIn multiplexes events from N subscribers onto out. It returns when
// stop is closed; partial delivery does not occur (any in-flight read
// is delivered before returning).
//
// We use reflect.Select because the number of subscribers is dynamic.
// A typical alfred Pod has <=8 sessions so the small constant overhead
// is fine.
func FanIn(subs []NamedSubscriber, out chan<- FanInEvent, stop <-chan struct{}) {
	cases := make([]reflect.SelectCase, 0, len(subs)+1)
	cases = append(cases, reflect.SelectCase{
		Dir:  reflect.SelectRecv,
		Chan: reflect.ValueOf(stop),
	})
	for _, s := range subs {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(s.Sub.C),
		})
	}
	for {
		idx, v, ok := reflect.Select(cases)
		if idx == 0 {
			return // stop closed
		}
		if !ok {
			// One subscriber's channel closed (e.g., shell.Shell shut down).
			// Remove it from cases and continue. Cases at indices > 0
			// correspond to subs[idx-1]; rebuild without that one.
			subs = append(subs[:idx-1], subs[idx:]...)
			cases = append(cases[:idx], cases[idx+1:]...)
			if len(subs) == 0 {
				return
			}
			continue
		}
		ev := v.Interface().(shell.CommandEvent)
		select {
		case out <- FanInEvent{SessionID: subs[idx-1].SessionID, Event: ev}:
		case <-stop:
			return
		}
	}
}
