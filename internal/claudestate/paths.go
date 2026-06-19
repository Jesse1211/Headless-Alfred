package claudestate

import (
	"path/filepath"
)

// SnapshotPath returns the on-disk location of the claude.json snapshot
// for a given Alfred session. dataDir is the same root the session
// manager already uses (~/.alfred or similar). Centralised here so
// every consumer agrees on the layout — and so tests don't recompute
// the path by hand.
//
// Panics on an empty session id. That's a programmer error: a caller
// shouldn't be asking for the snapshot path of "nothing." Erroring at
// the boundary keeps later code simpler (no nil checks downstream).
func SnapshotPath(dataDir, sessionID string) string {
	if sessionID == "" {
		panic("claudestate.SnapshotPath: empty sessionID")
	}
	return filepath.Join(dataDir, "sessions", sessionID, "claude.json")
}
