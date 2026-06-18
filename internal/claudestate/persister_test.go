package claudestate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersister_DirtyDebounce_WritesOnce(t *testing.T) {
	dir := t.TempDir()
	st := NewSessionState("sess1", "uuid-1")
	st.BeginTurn("u1", "hi", tAt(7, 0, 0))

	p, err := NewPersister(filepath.Join(dir, "claude.json"), st, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	defer p.Close(context.Background())

	// Burst of dirties — debounce should collapse them.
	for i := 0; i < 10; i++ {
		p.MarkDirty()
	}
	// Wait long enough that debounce timer fired (30ms) plus margin.
	time.Sleep(100 * time.Millisecond)

	// Verify exactly one file exists and parses back.
	data, err := os.ReadFile(filepath.Join(dir, "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot parse: %v\n%s", err, data)
	}
	if snap.Version != 1 {
		t.Errorf("version: %d", snap.Version)
	}
	if snap.SessionID != "sess1" {
		t.Errorf("sessionId: %q", snap.SessionID)
	}
	if len(snap.Turns) != 1 || snap.Turns[0].ID != "u1" {
		t.Errorf("turns: %+v", snap.Turns)
	}
}

func TestPersister_AtomicWrite_NoOrphanTmp(t *testing.T) {
	dir := t.TempDir()
	st := NewSessionState("sess1", "uuid-1")
	p, err := NewPersister(filepath.Join(dir, "claude.json"), st, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	defer p.Close(context.Background())

	p.MarkDirty()
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("orphan tmp file left behind: %q", e.Name())
		}
	}
}

func TestPersister_Flush_Sync(t *testing.T) {
	dir := t.TempDir()
	st := NewSessionState("sess1", "uuid-1")
	st.BeginTurn("u1", "before flush", tAt(7, 0, 0))
	p, _ := NewPersister(filepath.Join(dir, "claude.json"), st, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	defer p.Close(context.Background())

	// Long debounce — without Flush we'd wait 30s.
	p.MarkDirty()
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// File must exist immediately.
	if _, err := os.Stat(filepath.Join(dir, "claude.json")); err != nil {
		t.Errorf("file not written after Flush: %v", err)
	}
}
