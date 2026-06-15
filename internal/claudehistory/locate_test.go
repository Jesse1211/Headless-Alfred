package claudehistory

import (
	"os"
	"path/filepath"
	"testing"
)

func setupFixture(t *testing.T, uuid string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	projDir := filepath.Join(home, ".claude", "projects", "some-encoded-dir")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, uuid+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocate_FindsByBasename(t *testing.T) {
	uuid := "289ce55a-5293-4a52-b76d-6e4299e6fc90"
	want := setupFixture(t, uuid)
	c := NewLocator()
	got, err := c.Locate("sid-A", uuid)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocate_MissingReturnsErrNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := NewLocator()
	_, err := c.Locate("sid-A", "does-not-exist")
	if !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist, got %v", err)
	}
}

func TestLocate_CachesAcrossCalls(t *testing.T) {
	uuid := "ab"
	setupFixture(t, uuid)
	c := NewLocator()
	first, _ := c.Locate("sid-A", uuid)
	// Move the HOME so a second walk would fail — proves we cached.
	t.Setenv("HOME", t.TempDir())
	second, _ := c.Locate("sid-A", uuid)
	if first != second {
		t.Errorf("cache miss: first=%q second=%q", first, second)
	}
}

func TestLocate_CacheInvalidatesIfPathGone(t *testing.T) {
	uuid := "ab"
	path := setupFixture(t, uuid)
	c := NewLocator()
	if _, err := c.Locate("sid-A", uuid); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Cache entry now stale; second call should walk again and
	// (since the file is gone) return os.ErrNotExist.
	_, err := c.Locate("sid-A", uuid)
	if !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist after unlink, got %v", err)
	}
}
