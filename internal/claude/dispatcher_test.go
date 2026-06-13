package claude

import (
	"sync"
	"testing"
	"time"
)

func TestDispatcher_RoutesToSubscriber(t *testing.T) {
	d := NewDispatcher()
	ch, unsub := d.SubscribeAsks("alfred-A")
	defer unsub()

	var denied []string
	autoDeny := func(id, reason string) { denied = append(denied, id+":"+reason) }
	onAsk := d.OnAsk(func(claudeID string) string {
		if claudeID == "claude-1" {
			return "alfred-A"
		}
		return ""
	}, autoDeny)

	onAsk(PendingRequest{ToolUseID: "tu-1", SessionID: "claude-1", ToolName: "Bash"})

	select {
	case got := <-ch:
		if got.ToolUseID != "tu-1" {
			t.Errorf("got ToolUseID=%q", got.ToolUseID)
		}
	case <-time.After(time.Second):
		t.Fatal("never received PendingRequest")
	}
	if len(denied) != 0 {
		t.Errorf("unexpected denies: %v", denied)
	}
}

func TestDispatcher_NoSubscriber_AutoDeny(t *testing.T) {
	d := NewDispatcher()
	var denied []string
	var mu sync.Mutex
	autoDeny := func(id, reason string) {
		mu.Lock()
		denied = append(denied, id+":"+reason)
		mu.Unlock()
	}
	onAsk := d.OnAsk(func(string) string { return "alfred-X" }, autoDeny)
	onAsk(PendingRequest{ToolUseID: "tu-1", SessionID: "claude-1"})

	mu.Lock()
	defer mu.Unlock()
	if len(denied) != 1 {
		t.Fatalf("expected 1 auto-deny, got %d", len(denied))
	}
	if denied[0][:5] != "tu-1:" {
		t.Errorf("auto-deny target wrong: %q", denied[0])
	}
}

func TestDispatcher_UnknownClaudeConvo_AutoDeny(t *testing.T) {
	d := NewDispatcher()
	var denied int
	autoDeny := func(string, string) { denied++ }
	onAsk := d.OnAsk(func(string) string { return "" }, autoDeny)
	onAsk(PendingRequest{ToolUseID: "tu-1", SessionID: "unknown"})
	if denied != 1 {
		t.Errorf("denied=%d, want 1", denied)
	}
}

func TestDispatcher_ResubscribeClosesPrior(t *testing.T) {
	d := NewDispatcher()
	chOld, _ := d.SubscribeAsks("A")
	chNew, unsub := d.SubscribeAsks("A")
	defer unsub()

	// Old channel should be closed by the re-subscribe.
	select {
	case _, ok := <-chOld:
		if ok {
			t.Error("old channel produced value, want closed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("old channel never closed")
	}

	onAsk := d.OnAsk(func(string) string { return "A" }, func(string, string) {})
	onAsk(PendingRequest{ToolUseID: "tu", SessionID: "C"})
	select {
	case got := <-chNew:
		if got.ToolUseID != "tu" {
			t.Errorf("got %q", got.ToolUseID)
		}
	case <-time.After(time.Second):
		t.Error("new channel did not receive")
	}
}

func TestDispatcher_FullBuffer_AutoDeny(t *testing.T) {
	d := NewDispatcher()
	_, unsub := d.SubscribeAsks("A") // buffer=4, we don't drain
	defer unsub()
	var denied int
	autoDeny := func(string, string) { denied++ }
	onAsk := d.OnAsk(func(string) string { return "A" }, autoDeny)

	// Fill the 4-deep buffer; the 5th call should auto-deny.
	for i := 0; i < 5; i++ {
		onAsk(PendingRequest{ToolUseID: "tu", SessionID: "C"})
	}
	if denied < 1 {
		t.Errorf("denied=%d, want >=1 (5th call should overflow)", denied)
	}
}
