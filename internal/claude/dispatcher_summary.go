package claude

import (
	"encoding/json"

	"github.com/jesseliu/headless-alfred/internal/summary"
)

// maxSummaryWriteBytes caps the content size of an auto-allowed
// Write to the summary path. The template asks Claude to keep the
// file under 1500 characters; 8 KB gives ample headroom and still
// defends against an attacker convincing Claude to dump a huge
// payload into the file without user review.
const maxSummaryWriteBytes = 8 * 1024

// isSummaryIO reports whether the pending tool request is a
// Read or (size-capped) Write of the canonical summary path for
// the matched Alfred session. Used by the dispatcher to bypass
// the WS approval card for the summary template's per-turn churn.
//
// Strict string-equal match on file_path. Path traversal is not
// possible because alfredSID is the session ID we already
// resolved on the server side, never user input.
func isSummaryIO(req PendingRequest, alfredSID, dataDir string) bool {
	wantPath := summary.Path(dataDir, alfredSID)
	switch req.ToolName {
	case "Read":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		return in.FilePath == wantPath
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		if in.FilePath != wantPath {
			return false
		}
		if len(in.Content) > maxSummaryWriteBytes {
			return false
		}
		return true
	}
	return false
}
