package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MigrateLegacyLayout detects the pre-multi-session layout
// (<dir>/commands/*.json + <dir>/outputs/*.log) and folds every
// command into a single new session with the given importedSessionID
// and name "Imported".
//
// Returns (imported=true, nil) when migration ran.
// Returns (imported=false, nil) when there was nothing to migrate
// (sessions.json already exists, or there are no legacy dirs).
//
// The detection guard is "sessions.json doesn't exist yet" — once
// we've ever written sessions.json, this is a no-op even if stale
// legacy dirs happen to remain.
func MigrateLegacyLayout(dir, importedSessionID string, createdAt time.Time) (bool, error) {
	sf := NewSessionsFile(dir)
	existing, err := sf.Load()
	if err != nil {
		return false, fmt.Errorf("load sessions.json: %w", err)
	}
	if existing != nil {
		// Already initialized; skip.
		return false, nil
	}

	legacyCmds := filepath.Join(dir, "commands")
	legacyOuts := filepath.Join(dir, "outputs")
	if _, err := os.Stat(legacyCmds); errors.Is(err, os.ErrNotExist) {
		// Fresh install. Nothing to migrate, nothing to write — caller
		// will Save() sessions.json once a real session is created.
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("stat legacy commands: %w", err)
	}

	store, err := New(dir)
	if err != nil {
		return false, fmt.Errorf("new store: %w", err)
	}

	entries, err := os.ReadDir(legacyCmds)
	if err != nil {
		return false, fmt.Errorf("readdir legacy commands: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		raw, err := os.ReadFile(filepath.Join(legacyCmds, e.Name()))
		if err != nil {
			// Surface I/O errors; skip parse errors silently.
			return false, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			// Stamp this skip in stderr so the operator notices.
			fmt.Fprintf(os.Stderr, "migrate: skipping malformed %s: %v\n", e.Name(), err)
			continue
		}
		rec.SessionID = importedSessionID
		if err := store.Save(importedSessionID, rec); err != nil {
			return false, fmt.Errorf("save migrated %s: %w", id, err)
		}
		legacyOut := filepath.Join(legacyOuts, id+".log")
		if data, err := os.ReadFile(legacyOut); err == nil {
			if err := store.WriteOutput(importedSessionID, id, data); err != nil {
				return false, fmt.Errorf("migrate output %s: %w", id, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("read legacy output %s: %w", id, err)
		}
	}

	// Persist the synthetic session entry.
	meta := SessionMeta{
		ID:        importedSessionID,
		Name:      "Imported",
		CreatedAt: createdAt,
	}
	if err := sf.Save([]SessionMeta{meta}); err != nil {
		return false, fmt.Errorf("save sessions.json: %w", err)
	}

	// Remove the legacy dirs only after a successful save.
	if err := os.RemoveAll(legacyCmds); err != nil {
		return false, fmt.Errorf("remove legacy commands: %w", err)
	}
	if err := os.RemoveAll(legacyOuts); err != nil {
		return false, fmt.Errorf("remove legacy outputs: %w", err)
	}
	return true, nil
}
