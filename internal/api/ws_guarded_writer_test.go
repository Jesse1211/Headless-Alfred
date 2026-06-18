package api

import (
	"errors"
	"sync"
	"testing"
)

// Once WriteJSON fails, every subsequent call returns errClientGone
// without touching the underlying conn. The first failure also closes
// the conn so the reader goroutine wakes — which is what eventually
// triggers runClientLoop's deferred takeAll() to SIGINT in-flight
// runners. Without this, a dead WS connection left the runner
// blocked on a full claudeEvents channel forever.
func TestGuardedWriter_FirstFailureClosesConnAndShortCircuits(t *testing.T) {
	var (
		writeCalls int
		closeCalls int
	)
	writeJSON := func(any) error {
		writeCalls++
		return errors.New("broken pipe")
	}
	close := func() error {
		closeCalls++
		return nil
	}
	w := guardedWriter(writeJSON, close)

	if err := w(OutMsg{Type: "ping"}); err == nil {
		t.Fatal("expected error from first write")
	}
	if writeCalls != 1 {
		t.Errorf("writeCalls = %d, want 1", writeCalls)
	}
	if closeCalls != 1 {
		t.Errorf("closeCalls = %d, want 1 (first failure should close conn)", closeCalls)
	}

	// Subsequent writes must not hit WriteJSON OR Close again.
	for i := 0; i < 3; i++ {
		if err := w(OutMsg{Type: "ping"}); !errors.Is(err, errClientGone) {
			t.Errorf("subsequent write %d: err = %v, want errClientGone", i, err)
		}
	}
	if writeCalls != 1 {
		t.Errorf("writeCalls bled past dead flag: %d", writeCalls)
	}
	if closeCalls != 1 {
		t.Errorf("closeCalls bled past dead flag: %d", closeCalls)
	}
}

// Concurrent writers share the same serialized mutex; the gorilla
// websocket.Conn forbids concurrent WriteJSON. Drive a burst from
// many goroutines and assert WriteJSON is never invoked from two
// goroutines at once.
func TestGuardedWriter_SerializesConcurrentWrites(t *testing.T) {
	var (
		inFlight int
		maxSeen  int
		mu       sync.Mutex
	)
	writeJSON := func(any) error {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	}
	w := guardedWriter(writeJSON, func() error { return nil })

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w(OutMsg{Type: "ping"})
		}()
	}
	wg.Wait()

	if maxSeen != 1 {
		t.Errorf("max concurrent writers = %d, want 1 (mutex serialization broken)", maxSeen)
	}
}
