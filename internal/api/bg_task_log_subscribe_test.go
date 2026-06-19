package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/claudebgtasks"
)

// --- helpers for subscribe tests ---

// makeSubscribeCtx creates a cancellable context that simulates WS lifetime.
func makeSubscribeCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(context.Background())
}

// stubWrite is a thread-safe fake write closure that collects OutMsg frames.
type stubWrite struct {
	mu     sync.Mutex
	frames []OutMsg
	ch     chan OutMsg // buffered; notified on every write
}

func newStubWrite(bufSize int) *stubWrite {
	return &stubWrite{ch: make(chan OutMsg, bufSize)}
}

func (s *stubWrite) write(msg OutMsg) error {
	s.mu.Lock()
	s.frames = append(s.frames, msg)
	s.mu.Unlock()
	select {
	case s.ch <- msg:
	default:
	}
	return nil
}

// waitForChunk blocks until a bg_task_stdout_chunk frame arrives on sw.ch
// or the deadline passes. Returns (frame, true) on success.
func waitForChunk(sw *stubWrite, deadline time.Duration) (OutMsg, bool) {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case msg := <-sw.ch:
			if msg.Type == TypeBgTaskStdoutChunk {
				return msg, true
			}
		case <-timer.C:
			return OutMsg{}, false
		}
	}
}

// makeOutputFile creates the output file path via claudebgtasks.OutputPath in a
// temp dir and returns (path, taskID).
func makeOutputFile(t *testing.T, dir, sessionUUID, taskID string) string {
	t.Helper()
	p := claudebgtasks.OutputPath(dir, sessionUUID, taskID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create the file empty so it exists for watcher.Add.
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return p
}

// --- payload decode helper ---

func decodeBgTaskChunkPayload(t *testing.T, msg OutMsg) BgTaskStdoutChunkPayload {
	t.Helper()
	b, err := json.Marshal(msg.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var p BgTaskStdoutChunkPayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal BgTaskStdoutChunkPayload: %v", err)
	}
	return p
}

// --- tests ---

// TestWS_SubscribeBgTaskLog_StreamsAppendedBytes verifies that after
// subscribing, bytes appended to the output file arrive as
// bg_task_stdout_chunk frames with correctly base64-encoded content.
// No time.Sleep: synchronisation is via channels.
func TestWS_SubscribeBgTaskLog_StreamsAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	const sessionUUID = "aaaaaaaa-bbbb-cccc-dddd-000000000001"
	const taskID = "task-stream"

	filePath := makeOutputFile(t, dir, sessionUUID, taskID)

	sw := newStubWrite(16)
	subs := newBgTaskLogSubs()

	ctx, cancel := makeSubscribeCtx(t)
	defer cancel()

	meta := &mockMetaResolver{uuid: sessionUUID}
	cwdr := &mockCWDResolver{cwd: dir}

	handleSubscribeBgTaskLog(ctx, SubscribeBgTaskLogPayload{TaskID: taskID}, subs, sw.write, "sess1", meta, cwdr)

	// Append "hello" to the file after subscribe.
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	msg, ok := waitForChunk(sw, 5*time.Second)
	if !ok {
		t.Fatal("timed out waiting for bg_task_stdout_chunk frame")
	}

	payload := decodeBgTaskChunkPayload(t, msg)
	if payload.TaskID != taskID {
		t.Errorf("taskId = %q, want %q", payload.TaskID, taskID)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.Bytes)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != "hello" {
		t.Errorf("bytes = %q, want %q", string(decoded), "hello")
	}
}

// TestWS_SubscribeBgTaskLog_CleansUpOnUnsubscribe verifies that after
// unsubscribing, further writes to the file produce no chunk frames.
// Uses a goroutine-exit channel (exposed via a test hook on the sub) to
// avoid arbitrary sleeps.
func TestWS_SubscribeBgTaskLog_CleansUpOnUnsubscribe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	const sessionUUID = "aaaaaaaa-bbbb-cccc-dddd-000000000002"
	const taskID = "task-unsub"

	filePath := makeOutputFile(t, dir, sessionUUID, taskID)

	sw := newStubWrite(16)
	subs := newBgTaskLogSubs()

	ctx, cancel := makeSubscribeCtx(t)
	defer cancel()

	meta := &mockMetaResolver{uuid: sessionUUID}
	cwdr := &mockCWDResolver{cwd: dir}

	handleSubscribeBgTaskLog(ctx, SubscribeBgTaskLogPayload{TaskID: taskID}, subs, sw.write, "sess1", meta, cwdr)

	// Unsubscribe — this closes the watcher and cancels the goroutine.
	done := handleUnsubscribeBgTaskLog(UnsubscribeBgTaskLogPayload{TaskID: taskID}, subs)

	// Wait for the goroutine to exit before writing, so we don't race.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not exit after unsubscribe")
	}

	// Now write to the file — no chunk should arrive.
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("after-unsub"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Drain any residual frames (none expected after the goroutine exit).
	// Use a select with a short timer to confirm silence.
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case msg := <-sw.ch:
			if msg.Type == TypeBgTaskStdoutChunk {
				p := decodeBgTaskChunkPayload(t, msg)
				t.Errorf("unexpected chunk after unsubscribe: %q", p.Bytes)
			}
		case <-timer.C:
			return // silence confirmed
		}
	}
}

// TestWS_SubscribeBgTaskLog_CleansUpOnDisconnect verifies that when the
// context (representing WS lifetime) is cancelled, the goroutine exits
// and the watcher is closed.
func TestWS_SubscribeBgTaskLog_CleansUpOnDisconnect(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	const sessionUUID = "aaaaaaaa-bbbb-cccc-dddd-000000000003"
	const taskID = "task-disconnect"

	makeOutputFile(t, dir, sessionUUID, taskID)

	sw := newStubWrite(8)
	subs := newBgTaskLogSubs()

	ctx, cancel := makeSubscribeCtx(t)

	meta := &mockMetaResolver{uuid: sessionUUID}
	cwdr := &mockCWDResolver{cwd: dir}

	handleSubscribeBgTaskLog(ctx, SubscribeBgTaskLogPayload{TaskID: taskID}, subs, sw.write, "sess1", meta, cwdr)

	// Simulate WS disconnect by cancelling the context.
	cancel()

	// Wait for the goroutine to exit via the sub's done channel.
	sub := subs.get(taskID)
	if sub == nil {
		t.Fatal("expected sub to be registered")
	}
	select {
	case <-sub.done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not exit after context cancel")
	}
}

// TestWS_SubscribeBgTaskLog_WatcherUnavailable verifies that when
// watcher.Add fails (file does not exist), a bg_task_stdout_chunk frame
// with Status:"watcher_unavailable" is emitted synchronously.
func TestWS_SubscribeBgTaskLog_WatcherUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	const sessionUUID = "aaaaaaaa-bbbb-cccc-dddd-000000000004"
	const taskID = "task-nofile"

	// Do NOT create the output file — this makes watcher.Add fail.

	sw := newStubWrite(8)
	subs := newBgTaskLogSubs()

	ctx, cancel := makeSubscribeCtx(t)
	defer cancel()

	meta := &mockMetaResolver{uuid: sessionUUID}
	cwdr := &mockCWDResolver{cwd: dir}

	handleSubscribeBgTaskLog(ctx, SubscribeBgTaskLogPayload{TaskID: taskID}, subs, sw.write, "sess1", meta, cwdr)

	// The watcher_unavailable frame must be emitted synchronously (no goroutine to wait on).
	msg, ok := waitForChunk(sw, 2*time.Second)
	if !ok {
		t.Fatal("expected watcher_unavailable chunk frame")
	}
	payload := decodeBgTaskChunkPayload(t, msg)
	if payload.Status != "watcher_unavailable" {
		t.Errorf("status = %q, want watcher_unavailable", payload.Status)
	}
	if payload.TaskID != taskID {
		t.Errorf("taskId = %q, want %q", payload.TaskID, taskID)
	}
}

// TestWS_SubscribeBgTaskLog_InvalidTaskID verifies that invalid task IDs are
// silently rejected (no chunk frame, no panic).
func TestWS_SubscribeBgTaskLog_InvalidTaskID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	sw := newStubWrite(8)
	subs := newBgTaskLogSubs()

	ctx, cancel := makeSubscribeCtx(t)
	defer cancel()

	meta := &mockMetaResolver{uuid: "aaaaaaaa-bbbb-cccc-dddd-000000000005"}
	cwdr := &mockCWDResolver{cwd: dir}

	invalid := []string{
		"..",
		"x" + "xxxxxxxxxxxxxxxxxxxxxxxxx", // 25 chars > 20
		"task.id",
		"task@id",
		"",
	}
	for _, id := range invalid {
		handleSubscribeBgTaskLog(ctx, SubscribeBgTaskLogPayload{TaskID: id}, subs, sw.write, "sess1", meta, cwdr)
	}

	// No chunk frames should arrive.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case msg := <-sw.ch:
		t.Errorf("unexpected frame for invalid taskID: %+v", msg)
	case <-timer.C:
		// silence confirmed
	}
}

// TestWS_BgTaskStdoutChunk_PayloadShape verifies the JSON envelope of a
// bg_task_stdout_chunk frame: type tag, taskId, and valid base64 bytes.
func TestWS_BgTaskStdoutChunk_PayloadShape(t *testing.T) {
	const taskID = "shape-test"
	raw := []byte("some output bytes")
	msg := OutMsg{
		Type: TypeBgTaskStdoutChunk,
		Payload: BgTaskStdoutChunkPayload{
			TaskID: taskID,
			Bytes:  base64.StdEncoding.EncodeToString(raw),
		},
	}

	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var typeVal string
	if err := json.Unmarshal(out["type"], &typeVal); err != nil {
		t.Fatalf("type: %v", err)
	}
	if typeVal != TypeBgTaskStdoutChunk {
		t.Errorf("type = %q, want %q", typeVal, TypeBgTaskStdoutChunk)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(out["payload"], &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	var tid string
	if err := json.Unmarshal(payload["taskId"], &tid); err != nil {
		t.Fatalf("taskId: %v", err)
	}
	if tid != taskID {
		t.Errorf("taskId = %q, want %q", tid, taskID)
	}

	var bytesStr string
	if err := json.Unmarshal(payload["bytes"], &bytesStr); err != nil {
		t.Fatalf("bytes: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(bytesStr)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Errorf("bytes = %q, want %q", decoded, raw)
	}
}
