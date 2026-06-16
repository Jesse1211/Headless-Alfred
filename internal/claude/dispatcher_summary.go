package claude

import "github.com/jesseliu/headless-alfred/internal/summary"

// maxSummaryWriteBytes caps the content size of an auto-allowed
// Write to the summary path. The template asks Claude to keep the
// file under 1500 characters; 8 KB gives ample headroom and still
// defends against an attacker convincing Claude to dump a huge
// payload into the file without user review.
const maxSummaryWriteBytes = 8 * 1024

// isSummaryIO reports whether the pending tool request is a Read or
// (size-capped) Write of the canonical summary path for the matched
// Alfred session. Used by the dispatcher to bypass the WS approval
// card for the summary template's per-turn churn.
//
// Strict string-equal match on file_path. Path traversal is not
// possible because alfredSID is the session ID we already resolved
// on the server side, never user input. We do NOT filepath.Clean
// the path because we want to deny anything that isn't the EXACT
// canonical string.
func isSummaryIO(req PendingRequest, alfredSID, dataDir string) bool {
	wantPath := summary.Path(dataDir, alfredSID)
	return isCanonicalIO(req, maxSummaryWriteBytes, func(p string) bool {
		return p == wantPath
	})
}
