package claudestate

import (
	"path/filepath"
	"testing"
)

func TestSnapshotPath_StructureIsStable(t *testing.T) {
	got := SnapshotPath("/data", "01KVBX535FVFNH6SHF8P5WZ54B")
	want := filepath.Join("/data", "sessions", "01KVBX535FVFNH6SHF8P5WZ54B", "claude.json")
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestSnapshotPath_RejectsEmptyID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty session id")
		}
	}()
	SnapshotPath("/data", "")
}
