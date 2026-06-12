package api

import (
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/shell"
)

func TestFanin_DeliversFromMultipleSubs(t *testing.T) {
	bcastA := shell.NewEventBroadcaster()
	bcastB := shell.NewEventBroadcaster()
	subA := bcastA.Subscribe(8)
	subB := bcastB.Subscribe(8)

	out := make(chan FanInEvent, 16)
	stop := make(chan struct{})
	go FanIn([]NamedSubscriber{
		{SessionID: "A", Sub: subA},
		{SessionID: "B", Sub: subB},
	}, out, stop)

	bcastA.Publish(shell.CommandEvent{Started: &shell.StartedEvent{CmdID: "1"}})
	bcastB.Publish(shell.CommandEvent{Started: &shell.StartedEvent{CmdID: "2"}})

	gotA, gotB := false, false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !(gotA && gotB) {
		select {
		case ev := <-out:
			if ev.SessionID == "A" && ev.Event.Started.CmdID == "1" {
				gotA = true
			}
			if ev.SessionID == "B" && ev.Event.Started.CmdID == "2" {
				gotB = true
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !gotA || !gotB {
		t.Fatalf("missing: A=%v B=%v", gotA, gotB)
	}
	close(stop)
}

func TestFanin_StopReturnsCleanly(t *testing.T) {
	bcast := shell.NewEventBroadcaster()
	sub := bcast.Subscribe(8)
	out := make(chan FanInEvent, 4)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		FanIn([]NamedSubscriber{{SessionID: "A", Sub: sub}}, out, stop)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FanIn did not return after stop closed")
	}
}
