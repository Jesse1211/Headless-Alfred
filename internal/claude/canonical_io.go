package claude

import "encoding/json"

// isCanonicalIO is the shared scaffold for auto-allowing Read/Write
// against a fixed "canonical" file path. The match policy is left
// to the caller (a closure over the resolved path or a regex) so
// each auto-allow surface keeps its own decision about what counts
// as "canonical" — summary uses exact string equality, recap uses
// directory + basename regex. They diverge deliberately and the
// helper does NOT try to homogenize them.
//
// matchPath is invoked with the file_path field as the tool sent it
// — no normalization or cleaning is done here. If the caller wants
// filepath.Clean semantics, it must do that inside matchPath. This
// is on purpose: the summary path predicate intentionally rejects
// any path that isn't the EXACT canonical string (no Clean), so
// "/data/summaries/./X.md" is denied even though it Cleans to the
// same target. A central "always Clean first" would loosen that.
//
// maxBytes applies to the Write case only. Read is always allowed
// when matchPath returns true.
func isCanonicalIO(req PendingRequest, maxBytes int, matchPath func(string) bool) bool {
	switch req.ToolName {
	case "Read":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		return matchPath(in.FilePath)
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		if !matchPath(in.FilePath) {
			return false
		}
		return len(in.Content) <= maxBytes
	}
	return false
}
