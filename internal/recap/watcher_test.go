package recap

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcher_FiresOnWriteWithDate(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var dates []string
	w, err := StartWatcher(dir, func(date string) {
		mu.Lock()
		dates = append(dates, date)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	path := filepath.Join(Dir(dir), "2026-06-15.md")
	if err := os.WriteFile(path, []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(dates)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dates) == 0 || dates[0] != "2026-06-15" {
		t.Errorf("got %v, want [2026-06-15]", dates)
	}
}

func TestWatcher_SkipsNonDateFiles(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var fired int
	w, err := StartWatcher(dir, func(date string) {
		mu.Lock()
		fired++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	for _, name := range []string{
		".hidden.md",
		"2026-6-15.md",
		"2026-06-15",
		"2026-06-15.txt",
		"hello.md",
	} {
		if err := os.WriteFile(filepath.Join(Dir(dir), name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("fired = %d, want 0", fired)
	}
}
