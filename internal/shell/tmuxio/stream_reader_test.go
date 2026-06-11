package tmuxio

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// recordedSink captures bytes the StreamReader hands off to the parser.
type recordedSink struct {
	chunks []string
}

func (r *recordedSink) Feed(b []byte) {
	r.chunks = append(r.chunks, string(b))
}

func TestStreamReader_ReadsFromOffsetZero_FreshFile(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	if err := os.WriteFile(stream, []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	sink := &recordedSink{}
	sr, err := NewStreamReader(stream, offsetFile, sink)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer sr.Close()
	n, err := sr.ReadOnce()
	if err != nil {
		t.Fatalf("ReadOnce: %v", err)
	}
	if n != 5 {
		t.Fatalf("want 5 bytes, got %d", n)
	}
	if len(sink.chunks) != 1 || sink.chunks[0] != "hello" {
		t.Fatalf("sink chunks: %v", sink.chunks)
	}
	// Offset persisted to disk.
	got := readOffset(t, offsetFile)
	if got != 5 {
		t.Fatalf("offset = %d, want 5", got)
	}
}

func TestStreamReader_ResumesFromPersistedOffset(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	if err := os.WriteFile(stream, []byte("hello world"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(offsetFile, []byte("6"), 0o600); err != nil {
		t.Fatalf("seed offset: %v", err)
	}
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	n, _ := sr.ReadOnce()
	if n != 5 {
		t.Fatalf("want 5 (the trailing 'world'), got %d", n)
	}
	if sink.chunks[0] != "world" {
		t.Fatalf("chunk: %q", sink.chunks[0])
	}
}

func TestStreamReader_ReadOnceReturnsZeroWhenNoNewBytes(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	_ = os.WriteFile(stream, []byte("x"), 0o600)
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	_, _ = sr.ReadOnce() // consume the 'x'
	n, err := sr.ReadOnce()
	if err != nil {
		t.Fatalf("ReadOnce: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 new bytes, got %d", n)
	}
	if len(sink.chunks) != 1 {
		t.Fatalf("sink should have 1 chunk total, got %d", len(sink.chunks))
	}
}

func TestStreamReader_MissingStream_CreatesEmpty(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	sink := &recordedSink{}
	sr, err := NewStreamReader(stream, offsetFile, sink)
	if err != nil {
		t.Fatalf("NewStreamReader on missing files should not error, got %v", err)
	}
	defer sr.Close()
	if _, err := os.Stat(stream); err != nil {
		t.Fatalf("stream file not created: %v", err)
	}
	n, _ := sr.ReadOnce()
	if n != 0 {
		t.Fatalf("fresh stream, want 0 bytes, got %d", n)
	}
}

func TestStreamReader_TruncateAtIdleBoundary(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	_ = os.WriteFile(stream, []byte("AAAAAAAA BBBBBBBB"), 0o600) // 17 bytes
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	_, _ = sr.ReadOnce()
	// Run truncate with a FakeRunner so we can observe the pipe stop/restart
	// sequence (spec §4.4: this is racy in principle; we narrow the window by
	// always going stop-pipe → truncate → restart-pipe).
	fr := NewFakeRunner()
	_ = fr.NewSession("sess-1", "bash")
	if err := sr.TruncateConsumed(fr, "sess-1", "cat >> "+stream); err != nil {
		t.Fatalf("TruncateConsumed: %v", err)
	}
	info, err := os.Stat(stream)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("stream not truncated: size=%d", info.Size())
	}
	if got := readOffset(t, offsetFile); got != 0 {
		t.Fatalf("offset after truncate = %d, want 0", got)
	}
	// Verify the FakeRunner saw pipe-stop, then pipe-restart, in order.
	calls := fr.Calls()
	pipePaneCalls := []Call{}
	for _, c := range calls {
		if c.Method == "PipePane" {
			pipePaneCalls = append(pipePaneCalls, c)
		}
	}
	if len(pipePaneCalls) != 2 {
		t.Fatalf("want 2 PipePane calls (stop+restart), got %d: %+v", len(pipePaneCalls), pipePaneCalls)
	}
	if pipePaneCalls[0].Args[1] != "" {
		t.Fatalf("first PipePane should pass empty cmd to stop pipe, got %q", pipePaneCalls[0].Args[1])
	}
	if pipePaneCalls[1].Args[1] == "" {
		t.Fatalf("second PipePane should restart with non-empty cmd, got empty")
	}
	// Next write through Append simulates tmux continuing to write.
	f, _ := os.OpenFile(stream, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write([]byte("XYZ"))
	_ = f.Close()
	n, _ := sr.ReadOnce()
	if n != 3 || sink.chunks[len(sink.chunks)-1] != "XYZ" {
		t.Fatalf("post-truncate read failed: n=%d chunks=%v", n, sink.chunks)
	}
}

func TestStreamReader_OffsetFileAtomicallyWritten(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "pty.stream")
	offsetFile := filepath.Join(dir, "pty.offset")
	_ = os.WriteFile(stream, []byte("hi"), 0o600)
	sink := &recordedSink{}
	sr, _ := NewStreamReader(stream, offsetFile, sink)
	defer sr.Close()
	_, _ = sr.ReadOnce()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func readOffset(t *testing.T, path string) int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read offset: %v", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		t.Fatalf("parse offset %q: %v", data, err)
	}
	return n
}
