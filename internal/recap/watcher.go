package recap

import (
	"regexp"
	"time"

	"github.com/jesseliu/headless-alfred/internal/diskwatcher"
)

// Watcher type aliased for callers.
type Watcher = diskwatcher.Watcher[string]

var recapFilename = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2})\.md$`)

func StartWatcher(dataDir string, onWrite func(date string)) (*Watcher, error) {
	return diskwatcher.Start(
		Dir(dataDir),
		parseRecapFilename,
		200*time.Millisecond,
		onWrite,
		"recap watcher",
	)
}

// parseRecapFilename returns (date, true) for <YYYY-MM-DD>.md.
func parseRecapFilename(name string) (string, bool) {
	m := recapFilename.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}
