// Package claudebgtasks provides helpers for managing the file-system
// paths and environment variables that bind alfred-server's view of
// background-task output files to the Claude CLI's view.
package claudebgtasks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Bootstrap establishes the two env vars that bind alfred-server's
// view of bg-task file paths to the CLI's. Call once from main()
// after the data dir is known and before any claude -p is spawned.
//
// CLAUDE_CODE_TMPDIR is read by Claude CLI to decide where to put
// its per-uid scratch root. ALFRED_CLAUDE_BG_TASK_DIR is read by
// our path resolver to find log files. Setting both here keeps them
// from drifting.
//
// If the user has set ALFRED_CLAUDE_BG_TASK_DIR explicitly, that
// override wins (we do NOT also override CLAUDE_CODE_TMPDIR in that
// case — operator is responsible for consistency).
func Bootstrap(dataDir string) error {
	bgTmpRoot := filepath.Join(dataDir, "claude-tmp")
	if err := os.MkdirAll(bgTmpRoot, 0700); err != nil {
		return fmt.Errorf("create bg tmp root: %w", err)
	}
	if existing := os.Getenv("ALFRED_CLAUDE_BG_TASK_DIR"); existing != "" {
		return nil // operator-overridden
	}
	if err := os.Setenv("CLAUDE_CODE_TMPDIR", bgTmpRoot); err != nil {
		return fmt.Errorf("set CLAUDE_CODE_TMPDIR: %w", err)
	}
	uid := os.Getuid()
	bgTaskDir := filepath.Join(bgTmpRoot, "claude-"+strconv.Itoa(uid))
	if err := os.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", bgTaskDir); err != nil {
		return fmt.Errorf("set ALFRED_CLAUDE_BG_TASK_DIR: %w", err)
	}
	return nil
}
