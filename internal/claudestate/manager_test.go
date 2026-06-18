package claudestate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// GetOrLoad returns a stable *SessionState across calls — caching
// behaviour is essential for downstream consumers like the WS loop
// that look up the same session many times per second.
func TestSessionManager_GetOrLoad_ReturnsSameInstance(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &fakeJsonlLocator{})
	defer m.Shutdown(context.Background())

	a, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("expected the same *SessionState across GetOrLoad calls")
	}
}

// SingleFlight collapses concurrent first-access into one Load.
func TestSessionManager_GetOrLoad_SingleflightUnderContention(t *testing.T) {
	dir := t.TempDir()
	loc := &fakeJsonlLocator{}
	loc.delay = 25 * time.Millisecond
	m := NewSessionManager(dir, loc)
	defer m.Shutdown(context.Background())

	var wg sync.WaitGroup
	const N = 100
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = m.GetOrLoad("sess-x", "uuid-x")
		}()
	}
	wg.Wait()
	if got := loc.calls.Load(); got != 1 {
		t.Errorf("locator called %d times; want 1 (singleflight collapsed)", got)
	}
}

// Shutdown flushes the per-session Persister and prevents further
// GetOrLoad calls.
func TestSessionManager_Shutdown_FlushesAndCloses(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &fakeJsonlLocator{})
	st, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	st.BeginTurn("u1", "hi", time.Now().UTC())

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Snapshot file must exist on disk.
	snap := SnapshotPath(dir, "sess1")
	if _, err := os.Stat(snap); err != nil {
		t.Errorf("snapshot missing post-shutdown: %v", err)
	}
	// Post-shutdown GetOrLoad fails.
	if _, err := m.GetOrLoad("sess2", "uuid-2"); err == nil {
		t.Error("post-shutdown GetOrLoad should return error")
	}
}

// ---- fakes ----

type fakeJsonlLocator struct {
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeJsonlLocator) Locate(claudeUUID string) (string, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	// Return a path that doesn't exist; the loader degrades gracefully
	// to empty state when jsonl is missing.
	return filepath.Join(os.TempDir(), "nonexistent.jsonl"), nil
}
