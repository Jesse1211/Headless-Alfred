package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionsFile_LoadMissing_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	list, err := sf.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty, got %+v", list)
	}
}

func TestSessionsFile_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	now := time.Now().UTC().Truncate(time.Second)
	in := []SessionMeta{
		{ID: "a", Name: "Session 1", CreatedAt: now},
		{ID: "b", Name: "training", CreatedAt: now.Add(time.Minute)},
	}
	if err := sf.Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := sf.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out) != 2 || out[0].ID != "a" || out[1].Name != "training" {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestSessionsFile_SaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	if err := sf.Save([]SessionMeta{{ID: "a"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover tmp: %s", e.Name())
		}
	}
}

func TestSessionsFile_LoadMalformed_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	sf := NewSessionsFile(dir)
	_ = os.WriteFile(filepath.Join(dir, "sessions.json"), []byte("not json"), 0o600)
	_, err := sf.Load()
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}
