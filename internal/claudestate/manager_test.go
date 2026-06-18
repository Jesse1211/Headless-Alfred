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

// DeleteSession removes the in-memory entry, tears down the
// persister WITHOUT a final write (the upstream store has already
// removed the snapshot dir), and makes a subsequent GetOrLoad
// construct a fresh SessionState (not return the stale one).
// Without this hook the SessionState leaks until process shutdown —
// and worse, its Persister keeps a flock on a file in a deleted dir.
func TestSessionManager_DeleteSession_FreesEntry(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &fakeJsonlLocator{})
	defer m.Shutdown(context.Background())

	a, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	a.BeginTurn("u1", "hi", time.Now().UTC())

	if err := m.DeleteSession(context.Background(), "sess1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// Subsequent GetOrLoad must allocate a fresh instance — the old
	// one has been Closed.
	b, err := m.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("expected a fresh *SessionState after DeleteSession; got the cached one back")
	}
}

// Deleting a session that was never GetOrLoad'd is a no-op.
func TestSessionManager_DeleteSession_UnknownIsNoop(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &fakeJsonlLocator{})
	defer m.Shutdown(context.Background())

	if err := m.DeleteSession(context.Background(), "never-loaded"); err != nil {
		t.Errorf("DeleteSession on unknown session returned %v, want nil", err)
	}
}

// FinalizeAllInFlight closes out any zombie turn before shutdown so
// the on-disk snapshot reflects the truth. Without this we'd rely
// solely on the next-boot finalizeStaleTrailingTurn fallback —
// which works, but means the snapshot lives in a "lying" state on
// disk between boots.
func TestSessionManager_FinalizeAllInFlight_ClosesInFlightTurns(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &fakeJsonlLocator{})
	defer m.Shutdown(context.Background())

	a, _ := m.GetOrLoad("sess-with-inflight", "uuid-1")
	a.BeginTurn("u1", "hi", time.Now().UTC())

	b, _ := m.GetOrLoad("sess-idle", "uuid-2")
	_ = b // intentionally no BeginTurn; should be left alone

	m.FinalizeAllInFlight("server shutting down")

	a.View(func(s *ClaudeState) {
		if s.InFlight {
			t.Error("expected InFlight=false after FinalizeAllInFlight")
		}
		if !s.Turns[0].Done {
			t.Error("expected trailing turn Done=true")
		}
		if !s.Turns[0].IsError {
			t.Error("expected trailing turn IsError=true")
		}
		if s.LastError == nil || s.LastError.Code != "server_shutdown" {
			t.Errorf("expected LastError.Code=server_shutdown, got %+v", s.LastError)
		}
	})

	// Idle session must not gain a turn.
	b.View(func(s *ClaudeState) {
		if len(s.Turns) != 0 {
			t.Errorf("idle session got mutated: %d turns", len(s.Turns))
		}
		if s.LastError != nil {
			t.Errorf("idle session got LastError: %+v", s.LastError)
		}
	})
}

// ---- fakes ----

type fakeJsonlLocator struct {
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeJsonlLocator) Locate(sessionID, claudeUUID string) (string, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	// Return a path that doesn't exist; the loader degrades gracefully
	// to empty state when jsonl is missing.
	return filepath.Join(os.TempDir(), "nonexistent.jsonl"), nil
}
