package recap

import (
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	got := Dir("/data")
	want := filepath.Join("/data", "recaps")
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestPath(t *testing.T) {
	got := Path("/data", "2026-06-15")
	want := filepath.Join("/data", "recaps", "2026-06-15.md")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}
