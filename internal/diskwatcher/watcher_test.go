package diskwatcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func parseSidMd(name string) (string, bool) {
	if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
		return "", false
	}
	sid := strings.TrimSuffix(name, ".md")
	if sid == "" {
		return "", false
	}
	return sid, true
}

func TestStart_FiresOnWriteWithKey(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var keys []string
	w, err := Start(dir, parseSidMd, 50*time.Millisecond, func(k string) {
		mu.Lock()
		keys = append(keys, k)
		mu.Unlock()
	}, "test watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	if err := os.WriteFile(filepath.Join(dir, "sid-A.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(keys)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) == 0 || keys[0] != "sid-A" {
		t.Errorf("got %v, want [sid-A]", keys)
	}
}

func TestStart_RejectsNilCallbacks(t *testing.T) {
	dir := t.TempDir()
	if _, err := Start(dir, parseSidMd, 50*time.Millisecond, nil, "x"); err == nil {
		t.Error("want error when onWrite is nil")
	}
	if _, err := Start[string](dir, nil, 50*time.Millisecond, func(string) {}, "x"); err == nil {
		t.Error("want error when parse is nil")
	}
}

func TestStart_ParseRejectsSkips(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var n int
	w, err := Start(dir, parseSidMd, 50*time.Millisecond, func(string) {
		mu.Lock()
		n++
		mu.Unlock()
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	for _, name := range []string{".hidden.md", "no-ext", "x.txt"} {
		_ = os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if n != 0 {
		t.Errorf("fired = %d, want 0", n)
	}
}

func TestStop_CancelsPendingTimers(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var fired int
	w, err := Start(dir, parseSidMd, 200*time.Millisecond, func(string) {
		mu.Lock()
		fired++
		mu.Unlock()
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sid-A.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stop BEFORE the 200ms debounce fires; timer should be cancelled.
	time.Sleep(20 * time.Millisecond)
	w.Stop()
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired > 1 {
		t.Errorf("fired = %d, want 0 or 1 (timer must NOT fire after Stop)", fired)
	}
}
