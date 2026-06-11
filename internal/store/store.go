package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

var ErrNotFound = errors.New("record not found")

// Store owns the filesystem layout:
//
//	<dir>/sessions/<sessionID>/commands/<cmdID>.json
//	<dir>/sessions/<sessionID>/outputs/<cmdID>.log
//
// Every method takes a sessionID. Pass the same value used in
// Record.SessionID; the store does no cross-validation (callers are
// expected to know their own session).
type Store struct {
	dir string
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir sessions: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

// SessionDir returns the absolute path of the session's root directory.
// The directory may not exist yet; callers ensure that via Save/WriteOutput
// (both call ensureSessionDirs internally).
func (s *Store) SessionDir(sessionID string) string {
	return filepath.Join(s.dir, "sessions", sessionID)
}

func (s *Store) commandPath(sessionID, id string) string {
	return filepath.Join(s.SessionDir(sessionID), "commands", id+".json")
}

func (s *Store) outputPath(sessionID, id string) string {
	return filepath.Join(s.SessionDir(sessionID), "outputs", id+".log")
}

func (s *Store) ensureSessionDirs(sessionID string) error {
	for _, sub := range []string{"commands", "outputs"} {
		if err := os.MkdirAll(filepath.Join(s.SessionDir(sessionID), sub), 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	return nil
}

// EnsureSessionDirs creates the session's commands/ and outputs/
// subdirectories if absent. Exposed so callers can prepare the
// session-rooted layout before any Save runs (Plan 4's Manager does
// this just before launching the TmuxShell's read loop, which opens
// the stream file inside the session dir).
func (s *Store) EnsureSessionDirs(sessionID string) error {
	return s.ensureSessionDirs(sessionID)
}

// Save writes or overwrites the metadata file atomically (tmp + rename).
// The session's directory is created on demand.
func (s *Store) Save(sessionID string, r Record) error {
	if err := s.ensureSessionDirs(sessionID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	final := s.commandPath(sessionID, r.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (s *Store) Get(sessionID, id string) (Record, error) {
	data, err := os.ReadFile(s.commandPath(sessionID, id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// WriteOutput writes the entire output buffer for a command to its log file.
func (s *Store) WriteOutput(sessionID, id string, body []byte) error {
	if err := s.ensureSessionDirs(sessionID); err != nil {
		return err
	}
	return os.WriteFile(s.outputPath(sessionID, id), body, 0o600)
}

func (s *Store) OutputPath(sessionID, id string) string {
	return s.outputPath(sessionID, id)
}

// ReadOutput reads the output file for a command. Returns (nil, nil) if no
// output file exists yet (command may still be running or never had output).
func (s *Store) ReadOutput(sessionID, id string) ([]byte, error) {
	data, err := os.ReadFile(s.outputPath(sessionID, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// List returns records for the given session sorted by StartedAt descending.
// If before != "", only records strictly older than the one with that ID are
// returned. A session with no commands yet returns (nil, nil), not an error.
func (s *Store) List(sessionID string, limit int, before string) ([]Record, error) {
	dir := filepath.Join(s.SessionDir(sessionID), "commands")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		r, err := s.Get(sessionID, id)
		if err != nil {
			continue
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartedAt.After(all[j].StartedAt)
	})
	if before != "" {
		var beforeRec *Record
		for i := range all {
			if all[i].ID == before {
				beforeRec = &all[i]
				break
			}
		}
		if beforeRec != nil {
			filtered := all[:0]
			for _, r := range all {
				if r.StartedAt.Before(beforeRec.StartedAt) {
					filtered = append(filtered, r)
				}
			}
			all = filtered
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// SweepRunningToInterrupted scans every session and marks any record left
// in the "running" state as interrupted. Called once at boot for sessions
// whose bash is known to be gone (e.g., Pod-restart reconciliation).
//
// Pass an explicit list of sessionIDs to limit the sweep. An empty slice
// sweeps every session whose directory exists under sessions/.
func (s *Store) SweepRunningToInterrupted(sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		// Discover every session directory currently on disk.
		entries, err := os.ReadDir(filepath.Join(s.dir, "sessions"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				sessionIDs = append(sessionIDs, e.Name())
			}
		}
	}
	for _, sid := range sessionIDs {
		all, err := s.List(sid, 0, "")
		if err != nil {
			return fmt.Errorf("list %s: %w", sid, err)
		}
		for _, r := range all {
			if r.Status == StatusRunning {
				r.Status = StatusInterrupted
				if err := s.Save(sid, r); err != nil {
					return fmt.Errorf("sweep %s/%s: %w", sid, r.ID, err)
				}
			}
		}
	}
	return nil
}

// DeleteSession removes the entire session directory (commands + outputs).
// Idempotent: returns nil if the directory is already gone.
func (s *Store) DeleteSession(sessionID string) error {
	err := os.RemoveAll(s.SessionDir(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
