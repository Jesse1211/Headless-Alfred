package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return s
}

func TestStore_SaveAndGet(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := Record{
		ID:        "01HAB",
		SessionID: "sess-1",
		Command:   "ls",
		Cwd:       "/tmp",
		StartedAt: now,
		Status:    StatusRunning,
	}
	if err := s.Save(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Get("01HAB")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("session_id not round-tripped: %q", got.SessionID)
	}
	if got.Command != "ls" || got.Status != StatusRunning {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_SaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	rec := Record{ID: "A", Command: "x", Status: StatusRunning}
	if err := s.Save(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	// No .tmp files should remain after a successful save.
	entries, _ := os.ReadDir(filepath.Join(s.Dir(), "commands"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("missing")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_ListReturnsMostRecentFirst(t *testing.T) {
	s := newTestStore(t)
	for i, id := range []string{"A", "B", "C"} {
		rec := Record{
			ID:        id,
			Command:   id,
			Status:    StatusCompleted,
			StartedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := s.Save(rec); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		time.Sleep(10 * time.Millisecond) // ensure mtime ordering
	}
	list, err := s.List(10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	if list[0].ID != "C" || list[2].ID != "A" {
		t.Fatalf("order wrong: %+v", list)
	}
}

func TestStore_ListRespectsBefore(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"A", "B", "C", "D"} {
		_ = s.Save(Record{ID: id, Command: id, Status: StatusCompleted, StartedAt: time.Now().UTC()})
		time.Sleep(10 * time.Millisecond)
	}
	list, err := s.List(10, "C")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// "before=C" → returns records strictly older than C (A, B).
	gotIDs := make([]string, len(list))
	for i, r := range list {
		gotIDs[i] = r.ID
	}
	if len(list) != 2 || list[0].ID != "B" || list[1].ID != "A" {
		t.Fatalf("got %v, want [B A]", gotIDs)
	}
}

func TestStore_WriteAndReadOutput(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(Record{ID: "X", Command: "x", Status: StatusRunning})
	if err := s.WriteOutput("X", []byte("hello\nworld\n")); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	data, err := s.ReadOutput("X")
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if string(data) != "hello\nworld\n" {
		t.Fatalf("got %q", data)
	}
}

func TestStore_ReadOutput_MissingFile(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(Record{ID: "X", Command: "x", Status: StatusRunning})
	data, err := s.ReadOutput("X")
	if err != nil {
		t.Fatalf("ReadOutput on missing file should not error, got %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil, got %q", data)
	}
}

func TestStore_SweepMarksRunningAsInterrupted(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(Record{ID: "stuck", Status: StatusRunning, Command: "sleep"})
	_ = s.Save(Record{ID: "done", Status: StatusCompleted, Command: "ls"})
	if err := s.SweepRunningToInterrupted(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stuck, _ := s.Get("stuck")
	if stuck.Status != StatusInterrupted {
		t.Fatalf("stuck status = %s, want interrupted", stuck.Status)
	}
	done, _ := s.Get("done")
	if done.Status != StatusCompleted {
		t.Fatalf("done changed unexpectedly: %s", done.Status)
	}
}
