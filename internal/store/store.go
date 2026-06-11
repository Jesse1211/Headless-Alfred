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

type Store struct {
	dir string
}

func New(dir string) (*Store, error) {
	for _, sub := range []string{"commands", "outputs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) commandPath(id string) string {
	return filepath.Join(s.dir, "commands", id+".json")
}

func (s *Store) outputPath(id string) string {
	return filepath.Join(s.dir, "outputs", id+".log")
}

// Save writes or overwrites the metadata file atomically (tmp + rename).
func (s *Store) Save(r Record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	final := s.commandPath(r.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (s *Store) Get(id string) (Record, error) {
	data, err := os.ReadFile(s.commandPath(id))
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
// It does NOT touch the record metadata; the caller manages the Record.
func (s *Store) WriteOutput(id string, body []byte) error {
	return os.WriteFile(s.outputPath(id), body, 0o600)
}

// OutputPath returns the path that ReadOutput/WriteOutput use for the given
// command. Exposed so callers can compose absolute paths for logging or for
// external tools, without having to know the layout convention.
func (s *Store) OutputPath(id string) string {
	return s.outputPath(id)
}

// ReadOutput reads the output file for a command. Returns (nil, nil) if no
// output file exists yet (command may still be running or never had output).
func (s *Store) ReadOutput(id string) ([]byte, error) {
	data, err := os.ReadFile(s.outputPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// List returns records sorted by StartedAt descending. If before != "", only
// records strictly older than the one with that ID are returned.
func (s *Store) List(limit int, before string) ([]Record, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "commands"))
	if err != nil {
		return nil, err
	}
	var all []Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		r, err := s.Get(id)
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

// SweepRunningToInterrupted is called once at boot. Any record left in the
// "running" state from a previous process belongs to a bash that no longer
// exists, so it gets marked interrupted.
func (s *Store) SweepRunningToInterrupted() error {
	all, err := s.List(0, "")
	if err != nil {
		return err
	}
	for _, r := range all {
		if r.Status == StatusRunning {
			r.Status = StatusInterrupted
			if err := s.Save(r); err != nil {
				return fmt.Errorf("sweep %s: %w", r.ID, err)
			}
		}
	}
	return nil
}
