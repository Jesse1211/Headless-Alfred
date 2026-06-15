package claude

import (
	"encoding/json"
	"path/filepath"
	"regexp"

	"github.com/jesseliu/headless-alfred/internal/recap"
)

// maxRecapWriteBytes caps the content size of an auto-allowed Write
// to a recap path. 16 KB defends against a huge payload landing in
// the file without user review while leaving headroom — recaps span
// more sources than summaries.
const maxRecapWriteBytes = 16 * 1024

var recapBasename = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}\.md$`)

// isRecapIO reports whether the pending tool request is a Read or
// size-capped Write of a canonical recap path under <dataDir>/recaps/.
// Strict directory + basename match — no traversal possible because
// filepath.Clean normalizes and we re-check the parent matches the
// expected recap dir.
func isRecapIO(req PendingRequest, dataDir string) bool {
	wantDir := filepath.Clean(recap.Dir(dataDir))
	check := func(p string) bool {
		clean := filepath.Clean(p)
		if filepath.Dir(clean) != wantDir {
			return false
		}
		return recapBasename.MatchString(filepath.Base(clean))
	}
	switch req.ToolName {
	case "Read":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		return check(in.FilePath)
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(req.ToolInput, &in); err != nil {
			return false
		}
		if !check(in.FilePath) {
			return false
		}
		if len(in.Content) > maxRecapWriteBytes {
			return false
		}
		return true
	}
	return false
}
