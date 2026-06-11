package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrate_NoLegacyDirs_NoOp(t *testing.T) {
	dir := t.TempDir()
	imported, err := MigrateLegacyLayout(dir, "imported-id-A", time.Now().UTC())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if imported {
		t.Fatal("expected imported=false on a fresh dir, got true")
	}
}

func TestMigrate_LegacyDirsExist_FoldsIntoSession(t *testing.T) {
	dir := t.TempDir()
	// Seed the legacy layout.
	seedLegacyRecord(t, dir, "01HZA", `{"id":"01HZA","command":"ls","cwd":"/tmp","started_at":"2026-06-10T10:00:00Z","finished_at":"2026-06-10T10:00:01Z","exit_code":0,"output_truncated":false,"status":"completed"}`)
	seedLegacyRecord(t, dir, "01HZB", `{"id":"01HZB","command":"pwd","cwd":"/tmp","started_at":"2026-06-10T10:01:00Z","finished_at":"2026-06-10T10:01:01Z","exit_code":0,"output_truncated":false,"status":"completed"}`)
	seedLegacyOutput(t, dir, "01HZA", "tmp foo\n")
	seedLegacyOutput(t, dir, "01HZB", "/tmp\n")

	now := time.Now().UTC().Truncate(time.Second)
	imported, err := MigrateLegacyLayout(dir, "imp-1", now)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !imported {
		t.Fatal("expected imported=true")
	}

	// New layout: every command JSON moved + has session_id stamped.
	s, _ := New(dir)
	recA, err := s.Get("imp-1", "01HZA")
	if err != nil {
		t.Fatalf("Get 01HZA: %v", err)
	}
	if recA.SessionID != "imp-1" {
		t.Fatalf("session_id not stamped: %+v", recA)
	}
	if recA.Command != "ls" {
		t.Fatalf("command not preserved: %q", recA.Command)
	}
	outA, _ := s.ReadOutput("imp-1", "01HZA")
	if string(outA) != "tmp foo\n" {
		t.Fatalf("output not migrated: %q", outA)
	}

	// sessions.json has the imported entry.
	sf := NewSessionsFile(dir)
	list, _ := sf.Load()
	if len(list) != 1 || list[0].ID != "imp-1" || list[0].Name != "Imported" {
		t.Fatalf("sessions.json wrong: %+v", list)
	}

	// Legacy dirs are gone.
	if _, err := os.Stat(filepath.Join(dir, "commands")); !os.IsNotExist(err) {
		t.Fatalf("legacy commands/ still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outputs")); !os.IsNotExist(err) {
		t.Fatalf("legacy outputs/ still exists: %v", err)
	}
}

func TestMigrate_AlreadyMigrated_NoOp(t *testing.T) {
	dir := t.TempDir()
	// Pre-existing sessions.json signals "we've been here before".
	sf := NewSessionsFile(dir)
	_ = sf.Save([]SessionMeta{{ID: "pre", Name: "Existing", CreatedAt: time.Now().UTC()}})
	// Even with legacy dirs sitting around, migration shouldn't run.
	seedLegacyRecord(t, dir, "01HZX", `{"id":"01HZX","command":"ls"}`)
	imported, err := MigrateLegacyLayout(dir, "should-not-be-used", time.Now().UTC())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if imported {
		t.Fatal("expected imported=false when sessions.json already present")
	}
}

func TestMigrate_MalformedLegacyJSON_Skipped(t *testing.T) {
	dir := t.TempDir()
	seedLegacyRecord(t, dir, "01HZG", `{"id":"01HZG","command":"ok","status":"completed","started_at":"2026-06-10T10:00:00Z"}`)
	seedLegacyRecord(t, dir, "01HZB", `not json`) // malformed
	_, err := MigrateLegacyLayout(dir, "imp", time.Now().UTC())
	if err != nil {
		t.Fatalf("migrate should not fail on one malformed record: %v", err)
	}
	s, _ := New(dir)
	// The good record made it through.
	rec, err := s.Get("imp", "01HZG")
	if err != nil {
		t.Fatalf("Get good: %v", err)
	}
	if rec.Command != "ok" {
		t.Fatalf("good record corrupted: %+v", rec)
	}
	// The malformed one was skipped (Get returns NotFound).
	if _, err := s.Get("imp", "01HZB"); err != ErrNotFound {
		t.Fatalf("malformed record should be skipped, got %v", err)
	}
}

func seedLegacyRecord(t *testing.T, dir, id, jsonBody string) {
	t.Helper()
	// Sanity-check the fixture: legacy records were valid JSON in the
	// pre-multi-session schema. We accept malformed bytes too (for the
	// "skip bad records" test), so this only validates when the fixture
	// LOOKS like JSON (starts with '{').
	if len(jsonBody) > 0 && jsonBody[0] == '{' {
		var probe map[string]any
		if err := json.Unmarshal([]byte(jsonBody), &probe); err != nil {
			t.Fatalf("fixture is not valid JSON: %v", err)
		}
	}
	cmdsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(cmdsDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cmdsDir, id+".json"), []byte(jsonBody), 0o600); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
}

func seedLegacyOutput(t *testing.T, dir, id, body string) {
	t.Helper()
	outDir := filepath.Join(dir, "outputs")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy outputs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, id+".log"), []byte(body), 0o600); err != nil {
		t.Fatalf("write legacy output: %v", err)
	}
}
