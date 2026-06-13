package api

import "strings"

// validateGitCommit returns a non-empty user-facing error message when the
// command would invoke `git commit` without `-m`/`--message`.
//
// Why: bash on our PTY has no interactive stdin path back from the
// browser. `git commit` without a message launches $EDITOR (vi), which
// isn't installed and immediately leaves a stale .git/COMMIT_EDITMSG.
// Rather than let users discover this by trial, fail fast with a
// helpful error.
//
// We only catch the common shape: a command whose first token is "git"
// and second token is "commit". Compound forms like `cd repo && git
// commit` or `git -C repo commit` are not caught — that's fine. The
// goal is to help, not to be a full parser.
func validateGitCommit(command string) string {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return ""
	}
	if fields[0] != "git" || fields[1] != "commit" {
		return ""
	}
	for _, f := range fields[2:] {
		if f == "-m" || f == "--message" {
			return ""
		}
		if strings.HasPrefix(f, "-m") {
			// e.g. -m"msg" or -m=msg
			return ""
		}
		if strings.HasPrefix(f, "--message=") {
			return ""
		}
	}
	return `git commit requires -m "message" — interactive editor isn't available`
}
