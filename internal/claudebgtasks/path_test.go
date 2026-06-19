package claudebgtasks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMangleCwd_GoldenCases(t *testing.T) {
	// Compute the truncation case expected value programmatically.
	// Input: "/" + 200 'a's  = 201 chars total.
	// After mangle: "-" + 200 'a's = 201 chars → triggers truncation (> 200).
	// Truncated prefix: first 200 chars = "-" + 199 'a's.
	// Suffix: base-36(abs(javaHash("/" + 200 'a's))).
	truncInput := "/" + strings.Repeat("a", 200)
	h := javaHash(truncInput)
	if h < 0 {
		h = -h
	}
	truncExpected := "-" + strings.Repeat("a", 199) + "-" + strconv.FormatInt(int64(h), 36)

	cases := []struct {
		name, cwd, expected string
	}{
		{"deep_path", "/Users/jesseliu/Desktop/Chore/Headless-Alfred",
			"-Users-jesseliu-Desktop-Chore-Headless-Alfred"},
		{"tmp_realpath", "/private/tmp",
			"-private-tmp"},
		{"single_segment", "/Users/jesseliu",
			"-Users-jesseliu"},
		{"with_space_and_dot", "/path with space/foo.bar",
			"-path-with-space-foo-bar"},
		{"trailing_slash", "/Users/jesseliu/",
			"-Users-jesseliu-"},
		{"root", "/",
			"-"},
		{"truncation", truncInput, truncExpected},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MangleCwd(tc.cwd)
			if got != tc.expected {
				t.Errorf("MangleCwd(%q)\n  got  %q\n  want %q", tc.cwd, got, tc.expected)
			}
		})
	}
}

func TestOutputPath(t *testing.T) {
	realpathCwd := "/Users/jesseliu/Desktop/Chore/Headless-Alfred"
	sessionUUID := "01234567-89ab-cdef-0123-456789abcdef"
	taskID := "task-abc123"

	t.Run("env_unset_uses_tmp_uid", func(t *testing.T) {
		t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", "")
		got := OutputPath(realpathCwd, sessionUUID, taskID)

		expectedBase := filepath.Join("/tmp", "claude-"+strconv.Itoa(os.Getuid()))
		expectedMangled := MangleCwd(realpathCwd)
		expectedPath := filepath.Join(expectedBase, expectedMangled, sessionUUID, "tasks", taskID+".output")

		if got != expectedPath {
			t.Errorf("OutputPath (env unset)\n  got  %q\n  want %q", got, expectedPath)
		}
		if !strings.HasPrefix(got, fmt.Sprintf("/tmp/claude-%d/", os.Getuid())) {
			t.Errorf("OutputPath should start with /tmp/claude-<uid>/, got %q", got)
		}
	})

	t.Run("env_set_uses_custom_base", func(t *testing.T) {
		customBase := "/data/claude-bg"
		t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", customBase)
		got := OutputPath(realpathCwd, sessionUUID, taskID)

		expectedMangled := MangleCwd(realpathCwd)
		expectedPath := filepath.Join(customBase, expectedMangled, sessionUUID, "tasks", taskID+".output")

		if got != expectedPath {
			t.Errorf("OutputPath (env set)\n  got  %q\n  want %q", got, expectedPath)
		}
		if !strings.HasPrefix(got, customBase+"/") {
			t.Errorf("OutputPath should start with custom base %q, got %q", customBase, got)
		}
	})
}
