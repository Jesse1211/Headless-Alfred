// Package summary owns the per-session summary file: its on-disk
// path, the watcher that notices writes to it, and helpers shared
// by the prompt-injection path and the HTTP handler.
package summary

import "path/filepath"

// Path returns the on-disk summary path for the session.
// <dataDir>/summaries/<sessionID>.md
func Path(dataDir, sessionID string) string {
	return filepath.Join(Dir(dataDir), sessionID+".md")
}

// Dir returns the directory holding all summary files. Useful for
// fsnotify watchers and for tests that want to seed files.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "summaries")
}
