package notes

import (
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	got := Path("/data", "sid-A")
	want := filepath.Join("/data", "notes", "sid-A.md")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestDir(t *testing.T) {
	got := Dir("/data")
	want := filepath.Join("/data", "notes")
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}
