package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSession = "sess-1"

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
		SessionID: testSession,
		Command:   "ls",
		Cwd:       "/tmp",
		StartedAt: now,
		Status:    StatusRunning,
	}
	if err := s.Save(testSession, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Get(testSession, "01HAB")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != testSession {
		t.Fatalf("session_id not round-tripped: %q", got.SessionID)
	}
	if got.Command != "ls" || got.Status != StatusRunning {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_SaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	rec := Record{ID: "A", SessionID: testSession, Command: "x", Status: StatusRunning}
	if err := s.Save(testSession, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.SessionDir(testSession), "commands"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(testSession, "missing")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_ListReturnsMostRecentFirst(t *testing.T) {
	s := newTestStore(t)
	for i, id := range []string{"A", "B", "C"} {
		rec := Record{
			ID:        id,
			SessionID: testSession,
			Command:   id,
			Status:    StatusCompleted,
			StartedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := s.Save(testSession, rec); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	list, err := s.List(testSession, 10, "")
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
		_ = s.Save(testSession, Record{
			ID: id, SessionID: testSession, Command: id,
			Status: StatusCompleted, StartedAt: time.Now().UTC(),
		})
		time.Sleep(10 * time.Millisecond)
	}
	list, err := s.List(testSession, 10, "C")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
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
	_ = s.Save(testSession, Record{
		ID: "X", SessionID: testSession, Command: "x", Status: StatusRunning,
	})
	if err := s.WriteOutput(testSession, "X", []byte("hello\nworld\n")); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	data, err := s.ReadOutput(testSession, "X")
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if string(data) != "hello\nworld\n" {
		t.Fatalf("got %q", data)
	}
}

func TestStore_ReadOutput_MissingFile(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(testSession, Record{
		ID: "X", SessionID: testSession, Command: "x", Status: StatusRunning,
	})
	data, err := s.ReadOutput(testSession, "X")
	if err != nil {
		t.Fatalf("ReadOutput on missing file should not error, got %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil, got %q", data)
	}
}

func TestStore_SweepMarksRunningAsInterrupted(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(testSession, Record{
		ID: "stuck", SessionID: testSession, Status: StatusRunning, Command: "sleep",
	})
	_ = s.Save(testSession, Record{
		ID: "done", SessionID: testSession, Status: StatusCompleted, Command: "ls",
	})
	if err := s.SweepRunningToInterrupted([]string{testSession}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stuck, _ := s.Get(testSession, "stuck")
	if stuck.Status != StatusInterrupted {
		t.Fatalf("stuck status = %s, want interrupted", stuck.Status)
	}
	done, _ := s.Get(testSession, "done")
	if done.Status != StatusCompleted {
		t.Fatalf("done changed unexpectedly: %s", done.Status)
	}
}

func TestStore_ListIsolatedBySession(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save("sess-A", Record{ID: "1", SessionID: "sess-A", Command: "ls", Status: StatusCompleted, StartedAt: time.Now().UTC()})
	_ = s.Save("sess-B", Record{ID: "2", SessionID: "sess-B", Command: "pwd", Status: StatusCompleted, StartedAt: time.Now().UTC()})
	listA, _ := s.List("sess-A", 0, "")
	listB, _ := s.List("sess-B", 0, "")
	if len(listA) != 1 || listA[0].ID != "1" {
		t.Fatalf("sess-A list: %+v", listA)
	}
	if len(listB) != 1 || listB[0].ID != "2" {
		t.Fatalf("sess-B list: %+v", listB)
	}
}

func TestStore_DeleteSession_RemovesAllArtifacts(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(testSession, Record{ID: "1", SessionID: testSession, Command: "ls", Status: StatusCompleted, StartedAt: time.Now().UTC()})
	_ = s.WriteOutput(testSession, "1", []byte("out\n"))
	if err := s.DeleteSession(testSession); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(s.SessionDir(testSession)); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists: %v", err)
	}
	// Idempotent on missing.
	if err := s.DeleteSession(testSession); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestStore_List_UnknownSession_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.List("never-existed", 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list != nil {
		t.Fatalf("expected nil slice, got %+v", list)
	}
}

func TestStore_SessionDir_LayoutIsStable(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	got := s.SessionDir("01HX...")
	want := filepath.Join(dir, "sessions", "01HX...")
	if got != want {
		t.Fatalf("SessionDir = %q, want %q", got, want)
	}
}

func TestStore_EnsureSessionDirs_CreatesCommandsAndOutputs(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if err := s.EnsureSessionDirs("01HEX"); err != nil {
		t.Fatalf("EnsureSessionDirs: %v", err)
	}
	for _, sub := range []string{"commands", "outputs"} {
		path := filepath.Join(s.SessionDir("01HEX"), sub)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("%s missing or not a dir: %v", sub, err)
		}
	}
	// Idempotent — second call must not error.
	if err := s.EnsureSessionDirs("01HEX"); err != nil {
		t.Fatalf("second EnsureSessionDirs: %v", err)
	}
}
