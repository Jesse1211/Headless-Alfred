package shell

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestBroadcaster_DeliversToSubscriber(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(64)
	b.Publish([]byte("hello"))

	select {
	case msg := <-sub.C:
		if msg.Drop {
			t.Fatalf("unexpected drop marker")
		}
		if !bytes.Equal(msg.Bytes, []byte("hello")) {
			t.Fatalf("got %q want %q", msg.Bytes, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestBroadcaster_FanOutToMultipleSubscribers(t *testing.T) {
	b := NewBroadcaster()
	s1 := b.Subscribe(64)
	s2 := b.Subscribe(64)

	b.Publish([]byte("x"))

	for _, s := range []*Subscriber{s1, s2} {
		select {
		case msg := <-s.C:
			if !bytes.Equal(msg.Bytes, []byte("x")) {
				t.Fatalf("got %q", msg.Bytes)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
}

func TestBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(64)
	sub.Close()

	// Channel must be closed eventually.
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-sub.C:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel not closed after Close()")
		}
	}
}

func TestBroadcaster_SlowSubscriberGetsDropMarker(t *testing.T) {
	b := NewBroadcaster()
	slow := b.Subscribe(1) // tiny buffer
	fast := b.Subscribe(1024)

	// Publish more than slow can buffer.
	for i := 0; i < 100; i++ {
		b.Publish([]byte("chunk"))
	}

	// Fast subscriber should still get a delivery (publisher not blocked).
	select {
	case <-fast.C:
	case <-time.After(time.Second):
		t.Fatal("fast subscriber starved by slow one")
	}

	// Slow subscriber should eventually see a Drop marker.
	gotDrop := false
	deadline := time.After(time.Second)
loop:
	for {
		select {
		case msg, ok := <-slow.C:
			if !ok {
				break loop
			}
			if msg.Drop {
				gotDrop = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !gotDrop {
		t.Fatal("expected drop marker for slow subscriber, got none")
	}
}

func TestBroadcaster_ConcurrentPublishSafe(t *testing.T) {
	b := NewBroadcaster()
	sub := b.Subscribe(10000)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Publish([]byte("x"))
			}
		}()
	}
	wg.Wait()
	// Drain
	drained := 0
	for {
		select {
		case <-sub.C:
			drained++
		default:
			if drained < 1000 {
				t.Fatalf("expected 1000 messages, drained %d", drained)
			}
			return
		}
	}
}
