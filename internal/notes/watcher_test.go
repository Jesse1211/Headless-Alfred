package notes

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcher_FiresOnWriteWithSID(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var sids []string
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		sids = append(sids, sid)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	path := filepath.Join(Dir(dir), "sid-A.md")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sids)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sids) == 0 || sids[0] != "sid-A" {
		t.Errorf("got %v, want [sid-A]", sids)
	}
}

func TestWatcher_SkipsDotfilesAndNonMd(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var fired int
	w, err := StartWatcher(dir, func(sid string) {
		mu.Lock()
		fired++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	for _, name := range []string{".hidden.md", "no-ext", "with.txt"} {
		_ = os.WriteFile(filepath.Join(Dir(dir), name), []byte("x"), 0o644)
	}
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("fired = %d, want 0", fired)
	}
}
