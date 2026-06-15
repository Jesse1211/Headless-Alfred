package claudehistory

import (
	"os"
	"path/filepath"
	"sync"
)

// Locator finds the on-disk jsonl path for a Claude session uuid by
// walking ~/.claude/projects. Walks are bounded — local measurements
// at 752 jsonl files completed in ~80ms. The result is cached per
// alfred sessionID so subsequent refreshes skip the walk.
type Locator struct {
	mu    sync.Mutex
	cache map[string]string // sid → absolute path
}

func NewLocator() *Locator {
	return &Locator{cache: make(map[string]string)}
}

// Locate returns the absolute path of <uuid>.jsonl under ~/.claude/projects.
// Returns os.ErrNotExist if no matching file is found.
//
// A cached path is reused if its file still exists; if it was removed
// between calls (rare — uuid rotation or manual cleanup) we walk again.
func (l *Locator) Locate(sessionID, uuid string) (string, error) {
	l.mu.Lock()
	cached, ok := l.cache[sessionID]
	l.mu.Unlock()
	if ok {
		if _, err := os.Stat(cached); err == nil {
			return cached, nil
		}
		// stale; fall through to walk
		l.mu.Lock()
		delete(l.cache, sessionID)
		l.mu.Unlock()
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".claude", "projects")
	target := uuid + ".jsonl"

	var found string
	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// Permission-denied on a subtree — keep walking the rest.
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(p) == target {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return "", walkErr
	}
	if found == "" {
		return "", os.ErrNotExist
	}

	l.mu.Lock()
	l.cache[sessionID] = found
	l.mu.Unlock()
	return found, nil
}
