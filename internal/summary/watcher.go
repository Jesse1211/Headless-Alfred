package summary

import (
	"strings"
	"time"

	"github.com/jesseliu/headless-alfred/internal/diskwatcher"
)

// Watcher is the per-WS-connection summary file watcher. Re-exports
// the diskwatcher generic so call sites don't need to import the
// generic package.
type Watcher = diskwatcher.Watcher[string]

// StartWatcher creates the summaries directory if missing and
// dispatches debounced <sid>.md write/create events to onWrite.
// Same shape as the original — callers don't need to change.
func StartWatcher(dataDir string, onWrite func(sessionID string)) (*Watcher, error) {
	return diskwatcher.Start(
		Dir(dataDir),
		parseSummaryFilename,
		200*time.Millisecond,
		onWrite,
		"summary watcher",
	)
}

// parseSummaryFilename returns (sid, true) for <sid>.md, skipping
// dotfiles and non-md.
func parseSummaryFilename(name string) (string, bool) {
	if strings.HasPrefix(name, ".") {
		return "", false
	}
	if !strings.HasSuffix(name, ".md") {
		return "", false
	}
	sid := strings.TrimSuffix(name, ".md")
	if sid == "" {
		return "", false
	}
	return sid, true
}
