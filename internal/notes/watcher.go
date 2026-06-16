package notes

import (
	"strings"
	"time"

	"github.com/jesseliu/headless-alfred/internal/diskwatcher"
)

type Watcher = diskwatcher.Watcher[string]

func StartWatcher(dataDir string, onWrite func(sessionID string)) (*Watcher, error) {
	return diskwatcher.Start(
		Dir(dataDir),
		parseNotesFilename,
		200*time.Millisecond,
		onWrite,
		"notes watcher",
	)
}

func parseNotesFilename(name string) (string, bool) {
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
