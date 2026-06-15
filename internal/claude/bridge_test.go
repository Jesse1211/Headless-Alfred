package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// TestBridge_AllowFlow simulates the round-trip:
//
//	hook stdin → POST /tool-approval (blocks)
//	Resolve(tool_use_id, allow)
//	hook stdin gets {"permissionDecision":"allow"} in the HTTP response body
//
// The hook payload is a real one captured from the in-pod claude
// (see testdata/hook_pretooluse_bash.json).
func TestBridge_AllowFlow(t *testing.T) {
	body, err := os.ReadFile("testdata/hook_pretooluse_bash.json")
	if err != nil {
		t.Fatal(err)
	}

	var asks []PendingRequest
	var asksMu sync.Mutex
	b := NewBridge(func(req PendingRequest) {
		asksMu.Lock()
		asks = append(asks, req)
		asksMu.Unlock()
	})
	if err := b.Start(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()

	// Fire the request in a goroutine — handleAsk blocks until
	// Resolve is called.
	var (
		respBody []byte
		respErr  error
		respDone = make(chan struct{})
	)
	go func() {
		defer close(respDone)
		resp, err := http.Post("http://"+b.Addr()+"/tool-approval",
			"application/json", bytes.NewReader(body))
		if err != nil {
			respErr = err
			return
		}
		defer resp.Body.Close()
		respBody, respErr = io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			respErr = fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
		}
	}()

	// Give the request a moment to land in onAsk.
	deadline := time.Now().Add(2 * time.Second)
	for {
		asksMu.Lock()
		n := len(asks)
		asksMu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("onAsk was not called within 2s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	asksMu.Lock()
	got := asks[0]
	asksMu.Unlock()

	if got.ToolUseID == "" {
		t.Error("ToolUseID empty")
	}
	if got.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", got.ToolName)
	}
	var input map[string]string
	if err := json.Unmarshal(got.ToolInput, &input); err != nil {
		t.Fatalf("ToolInput unmarshal: %v", err)
	}
	if input["command"] != "echo hello" {
		t.Errorf("tool_input.command = %q, want 'echo hello'", input["command"])
	}

	// Now resolve.
	if !b.Resolve(got.ToolUseID, Decision{Permission: "allow"}) {
		t.Fatal("Resolve returned false")
	}

	<-respDone
	if respErr != nil {
		t.Fatalf("response error: %v", respErr)
	}
	dec := parseHookResponse(t, respBody)
	if dec["permissionDecision"] != "allow" {
		t.Errorf("permissionDecision = %q, want allow", dec["permissionDecision"])
	}
}

// parseHookResponse unwraps the {"hookSpecificOutput":{...}} envelope
// that the bridge emits so tests can assert on the inner fields.
func parseHookResponse(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var outer struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(body, &outer); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if outer.HookSpecificOutput == nil {
		t.Fatalf("missing hookSpecificOutput in response: %s", body)
	}
	return outer.HookSpecificOutput
}

// TestBridge_DenyIncludesReason ensures deny decisions carry the
// reason field through to the hook output, so claude can show the
// user why we blocked.
func TestBridge_DenyIncludesReason(t *testing.T) {
	body := mustReadFixture(t, "hook_pretooluse_bash.json")
	var b *Bridge
	b = NewBridge(func(req PendingRequest) {
		go func() {
			b.Resolve(req.ToolUseID, Decision{
				Permission: "deny",
				Reason:     "user denied",
			})
		}()
	})
	if err := b.Start(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()

	resp, err := http.Post("http://"+b.Addr()+"/tool-approval",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, out)
	}
	dec := parseHookResponse(t, out)
	if dec["permissionDecision"] != "deny" {
		t.Errorf("permissionDecision = %q, want deny", dec["permissionDecision"])
	}
	if dec["permissionDecisionReason"] != "user denied" {
		t.Errorf("permissionDecisionReason = %q, want 'user denied'",
			dec["permissionDecisionReason"])
	}
}

// TestBridge_Timeout ensures the bridge auto-denies after AskTimeout.
func TestBridge_Timeout(t *testing.T) {
	b := NewBridge(func(req PendingRequest) {})
	b.AskTimeout = 100 * time.Millisecond
	if err := b.Start(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()

	body := mustReadFixture(t, "hook_pretooluse_bash.json")
	resp, err := http.Post("http://"+b.Addr()+"/tool-approval",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	dec := parseHookResponse(t, out)
	if dec["permissionDecision"] != "deny" {
		t.Errorf("timeout decision = %q, want deny", dec["permissionDecision"])
	}
	if dec["permissionDecisionReason"] == "" {
		t.Error("timeout decision missing reason")
	}
}

// TestBridge_DuplicateToolUseID rejects a second concurrent ask for
// the same tool_use_id. (Defensive — claude shouldn't generate
// duplicate IDs in normal operation.)
func TestBridge_DuplicateToolUseID(t *testing.T) {
	b := NewBridge(func(req PendingRequest) {
		// Don't resolve — let the first one stay pending.
	})
	if err := b.Start(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()

	body := mustReadFixture(t, "hook_pretooluse_bash.json")

	// First POST: in-flight.
	go func() {
		_, _ = http.Post("http://"+b.Addr()+"/tool-approval",
			"application/json", bytes.NewReader(body))
	}()
	// Give it a moment to register.
	time.Sleep(50 * time.Millisecond)

	// Second POST with same body (same tool_use_id) should 409.
	resp, err := http.Post("http://"+b.Addr()+"/tool-approval",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second POST status = %d, want 409", resp.StatusCode)
	}
}

// TestBridge_BadBody returns 400 on malformed JSON.
func TestBridge_BadBody(t *testing.T) {
	b := NewBridge(func(req PendingRequest) {})
	if err := b.Start(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()

	resp, err := http.Post("http://"+b.Addr()+"/tool-approval",
		"application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestBridge_GETIsRejected — sanity check the API is POST-only.
func TestBridge_GETIsRejected(t *testing.T) {
	b := NewBridge(func(req PendingRequest) {})
	if err := b.Start(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	resp, err := http.Get("http://" + b.Addr() + "/tool-approval")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
