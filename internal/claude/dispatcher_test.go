package claude

import (
	"sync"
	"testing"
	"time"
)

// noAllow / noDeny are placeholder callbacks for tests that don't
// care about the path not being exercised.
func noAllow(string)         {}
func noDeny(string, string)  {}

func TestDispatcher_RoutesToSubscriber(t *testing.T) {
	d := NewDispatcher()
	ch, unsub := d.SubscribeAsks("alfred-A")
	defer unsub()

	var denied []string
	var allowed []string
	autoDeny := func(id, reason string) { denied = append(denied, id+":"+reason) }
	autoAllow := func(id string) { allowed = append(allowed, id) }
	onAsk := d.OnAsk(func(claudeID string) string {
		if claudeID == "claude-1" {
			return "alfred-A"
		}
		return ""
	}, autoAllow, autoDeny, "")

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
	if len(allowed) != 0 {
		t.Errorf("unexpected allows: %v", allowed)
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
	onAsk := d.OnAsk(func(string) string { return "alfred-X" }, noAllow, autoDeny, "")
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

// Unknown convo means a claude session that wasn't spawned by Alfred
// (think: user typing `claude` in another terminal). We must NOT deny
// — that would break their unrelated tools. Allow is the fail-open
// default. This test specifically guards against the regression that
// caused this very feature to lock the developer out of every tool.
func TestDispatcher_UnknownClaudeConvo_AutoAllow(t *testing.T) {
	d := NewDispatcher()
	var allowed int
	var denied int
	autoAllow := func(string) { allowed++ }
	autoDeny := func(string, string) { denied++ }
	onAsk := d.OnAsk(func(string) string { return "" }, autoAllow, autoDeny, "")
	onAsk(PendingRequest{ToolUseID: "tu-1", SessionID: "unknown"})
	if allowed != 1 {
		t.Errorf("allowed=%d, want 1", allowed)
	}
	if denied != 0 {
		t.Errorf("denied=%d, want 0 (foreign claude must NOT be denied)", denied)
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

	onAsk := d.OnAsk(func(string) string { return "A" }, noAllow, noDeny, "")
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
	onAsk := d.OnAsk(func(string) string { return "A" }, noAllow, autoDeny, "")

	// Fill the 4-deep buffer; the 5th call should auto-deny.
	for i := 0; i < 5; i++ {
		onAsk(PendingRequest{ToolUseID: "tu", SessionID: "C"})
	}
	if denied < 1 {
		t.Errorf("denied=%d, want >=1 (5th call should overflow)", denied)
	}
}

func TestDispatcher_SummaryWrite_AutoAllows(t *testing.T) {
	d := NewDispatcher()
	ch, unsub := d.SubscribeAsks("alfred-A")
	defer unsub()

	var allowed int
	autoAllow := func(string) { allowed++ }
	autoDeny := func(string, string) {}

	onAsk := d.OnAsk(
		func(s string) string {
			if s == "claude-1" {
				return "alfred-A"
			}
			return ""
		},
		autoAllow,
		autoDeny,
		"/data", // new dataDir argument
	)

	// Build a small Write to the canonical path.
	req := PendingRequest{
		ToolUseID: "tu-summary",
		SessionID: "claude-1",
		ToolName:  "Write",
		ToolInput: []byte(`{"file_path":"/data/summaries/alfred-A.md","content":"## Goal\nfoo"}`),
	}
	onAsk(req)

	if allowed != 1 {
		t.Errorf("allowed=%d, want 1 (auto-allow summary write)", allowed)
	}
	select {
	case got := <-ch:
		t.Errorf("summary write must NOT push to subscriber channel; got %+v", got)
	default:
	}
}
