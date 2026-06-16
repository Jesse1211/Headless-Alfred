// Package notes owns the per-session notes file: its on-disk path,
// the watcher that notices writes, and helpers shared by the HTTP
// handler. Notes are user-authored only — NEVER injected into a
// Claude prompt; this package has no read path into composePromptText.
package notes

import "path/filepath"

// Path returns the on-disk notes path for the session.
// <dataDir>/notes/<sessionID>.md
func Path(dataDir, sessionID string) string {
	return filepath.Join(Dir(dataDir), sessionID+".md")
}

// Dir returns the directory holding all notes files. Used by the
// fsnotify watcher and by tests that seed files.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "notes")
}
