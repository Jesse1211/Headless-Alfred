package summary

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcher_WriteFiresOnWriteCallback(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var got []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		got = append(got, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Write the file. summaries/ should exist (StartWatcher mkdir's it).
	if err := os.WriteFile(Path(dir, "S1"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Debounce window is 200ms; give it 600ms.
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "S1" {
		t.Errorf("got=%v, want [S1]", got)
	}
}

func TestWatcher_DebouncesBurstyWrites(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var got []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		got = append(got, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Fire 10 writes quickly on the same file.
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(Path(dir, "S2"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	// We want one event per filename per debounce window. Fewer
	// than the 10 writes that fsnotify would otherwise emit.
	if len(got) == 0 {
		t.Fatal("expected at least one callback, got none")
	}
	if len(got) > 2 {
		t.Errorf("expected debounce to coalesce 10 writes into <=2 callbacks; got %d (%v)", len(got), got)
	}
}

func TestWatcher_IgnoresNonMatchingFilenames(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var got []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		got = append(got, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// .txt / no extension / hidden file — all must be ignored.
	for _, name := range []string{"S1.txt", "S2", ".hidden.md"} {
		if err := os.WriteFile(filepath.Join(Dir(dir), name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Errorf("got=%v, want none — filenames don't match <sid>.md", got)
	}
}

func TestWatcher_FailsGracefullyIfMkdirDenied(t *testing.T) {
	// We can't easily simulate a denied mkdir cross-platform in
	// a test. Just assert StartWatcher returns an error on a
	// path under a file (not a directory).
	dir := t.TempDir()
	notADir := filepath.Join(dir, "blocker")
	if err := os.WriteFile(notADir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// `notADir` is a regular file; Dir(notADir) = notADir/summaries
	// which cannot be created.
	w, err := StartWatcher(notADir, func(string) {})
	if err == nil {
		t.Error("expected StartWatcher to return an error when mkdir fails")
		w.Stop()
	}
}
