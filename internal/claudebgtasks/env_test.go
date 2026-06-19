package claudebgtasks

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestBootstrap_DefaultCase(t *testing.T) {
	dataDir := t.TempDir()

	// Ensure neither env var is set at test start.
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", "")
	t.Setenv("CLAUDE_CODE_TMPDIR", "")

	if err := Bootstrap(dataDir); err != nil {
		t.Fatalf("Bootstrap returned unexpected error: %v", err)
	}

	wantTmpRoot := filepath.Join(dataDir, "claude-tmp")
	wantBgTaskDir := filepath.Join(wantTmpRoot, "claude-"+strconv.Itoa(os.Getuid()))

	if got := os.Getenv("CLAUDE_CODE_TMPDIR"); got != wantTmpRoot {
		t.Errorf("CLAUDE_CODE_TMPDIR = %q, want %q", got, wantTmpRoot)
	}
	if got := os.Getenv("ALFRED_CLAUDE_BG_TASK_DIR"); got != wantBgTaskDir {
		t.Errorf("ALFRED_CLAUDE_BG_TASK_DIR = %q, want %q", got, wantBgTaskDir)
	}
}

func TestBootstrap_OperatorOverride(t *testing.T) {
	dataDir := t.TempDir()
	customDir := "/custom/bg/task/dir"

	// Pre-set ALFRED_CLAUDE_BG_TASK_DIR to simulate operator override.
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", customDir)
	// Clear CLAUDE_CODE_TMPDIR so we can detect if Bootstrap touches it.
	t.Setenv("CLAUDE_CODE_TMPDIR", "")

	if err := Bootstrap(dataDir); err != nil {
		t.Fatalf("Bootstrap returned unexpected error: %v", err)
	}

	// ALFRED_CLAUDE_BG_TASK_DIR must keep the pre-set value.
	if got := os.Getenv("ALFRED_CLAUDE_BG_TASK_DIR"); got != customDir {
		t.Errorf("ALFRED_CLAUDE_BG_TASK_DIR = %q, want %q (operator value)", got, customDir)
	}
	// CLAUDE_CODE_TMPDIR must NOT have been modified.
	if got := os.Getenv("CLAUDE_CODE_TMPDIR"); got != "" {
		t.Errorf("CLAUDE_CODE_TMPDIR = %q, want empty (must not be set when operator override present)", got)
	}
}

func TestBootstrap_DirCreatedWithMode0700(t *testing.T) {
	dataDir := t.TempDir()

	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", "")
	t.Setenv("CLAUDE_CODE_TMPDIR", "")

	if err := Bootstrap(dataDir); err != nil {
		t.Fatalf("Bootstrap returned unexpected error: %v", err)
	}

	bgTmpRoot := filepath.Join(dataDir, "claude-tmp")
	info, err := os.Stat(bgTmpRoot)
	if err != nil {
		t.Fatalf("stat %q: %v", bgTmpRoot, err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", bgTmpRoot)
	}
	// Check permissions: mode bits (perm bits only, strip type bits).
	perm := info.Mode().Perm()
	if perm != 0700 {
		t.Errorf("bgTmpRoot mode = %04o, want 0700", perm)
	}
}
