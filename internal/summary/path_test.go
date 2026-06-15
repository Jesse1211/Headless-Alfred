package summary

import (
	"path/filepath"
	"testing"
)

func TestPath_JoinsDataDirAndSidWithMdExt(t *testing.T) {
	got := Path("/data", "01KSESSION")
	want := filepath.Join("/data", "summaries", "01KSESSION.md")
	if got != want {
		t.Errorf("Path=%q, want %q", got, want)
	}
}

func TestDir_IsSummariesUnderDataDir(t *testing.T) {
	got := Dir("/data")
	want := filepath.Join("/data", "summaries")
	if got != want {
		t.Errorf("Dir=%q, want %q", got, want)
	}
}
