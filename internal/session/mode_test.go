package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/store"
)

// TestCreate_DefaultsToShellMode verifies that every freshly created session
// starts in shell mode.
func TestCreate_DefaultsToShellMode(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("test-session")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := m.GetMode(meta.ID); got != store.ModeShell {
		t.Fatalf("GetMode after Create = %q, want %q", got, store.ModeShell)
	}
}

// TestSetMode_PersistsAcrossNewManager verifies that SetMode writes through
// to sessions.json so a restarted Manager reads the correct mode.
func TestSetMode_PersistsAcrossNewManager(t *testing.T) {
	m, fr := newTestManager(t)
	meta, err := m.Create("persist-test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := m.SetMode(meta.ID, store.ModeClaude); err != nil {
		t.Fatalf("SetMode: %v", err)
	}

	// Build a second Manager backed by the same data dir. We can reuse
	// the same FakeRunner — it already has the tmux session from Create.
	dir := m.cfg.DataDir
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	m2, err := NewManager(Config{
		DataDir:      dir,
		Store:        st,
		SessionsFile: store.NewSessionsFile(dir),
		Runner:       fr,
		Nonce:        "test-nonce",
		MaxSessions:  8,
		Logger:       m.cfg.Logger,
	})
	if err != nil {
		t.Fatalf("NewManager2: %v", err)
	}
	if err := m2.Reconcile(); err != nil {
		t.Fatalf("Reconcile2: %v", err)
	}

	if got := m2.GetMode(meta.ID); got != store.ModeClaude {
		t.Fatalf("GetMode after reload = %q, want %q", got, store.ModeClaude)
	}
}

// TestSetMode_UnknownSession_ReturnsError verifies that SetMode returns
// ErrSessionNotFound for non-existent session IDs.
func TestSetMode_UnknownSession_ReturnsError(t *testing.T) {
	m, _ := newTestManager(t)
	err := m.SetMode("does-not-exist", store.ModeClaude)
	if err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

// TestReconcile_ResetsClaudeToShell verifies that the "stored \ live"
// branch of Reconcile resets mode=claude back to shell (because any
// running Claude process died with the pod/tmux daemon).
func TestReconcile_ResetsClaudeToShell(t *testing.T) {
	m, _ := newTestManager(t)

	// Pre-populate sessions.json with a session whose mode is claude but
	// whose tmux session does NOT exist (simulates pod restart).
	id := "01HXBA"
	createdAt := time.Now().UTC().Add(-time.Hour)
	_ = m.cfg.SessionsFile.Save([]store.SessionMeta{
		{ID: id, Name: "ClaudeWas", CreatedAt: createdAt, Mode: store.ModeClaude},
	})
	// NOTE: do NOT call fr.NewSession(id, ...) — we want stored \ live.

	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// In-memory mode must be shell.
	if got := m.GetMode(id); got != store.ModeShell {
		t.Fatalf("GetMode after Reconcile = %q, want %q", got, store.ModeShell)
	}

	// sessions.json on disk must also record shell (or omit the field,
	// which the loader normalises to shell).
	persisted, err := m.cfg.SessionsFile.Load()
	if err != nil {
		t.Fatalf("SessionsFile.Load: %v", err)
	}
	var found bool
	for _, sm := range persisted {
		if sm.ID == id {
			found = true
			if sm.Mode != store.ModeShell {
				t.Fatalf("sessions.json mode = %q, want %q", sm.Mode, store.ModeShell)
			}
		}
	}
	if !found {
		t.Fatalf("session %s not found in persisted list: %+v", id, persisted)
	}
}

// TestSessionsJSONLoader_EmptyModeBecomesShell verifies that the Load
// normalisation converts "" (missing field) to ModeShell.
func TestSessionsJSONLoader_EmptyModeBecomesShell(t *testing.T) {
	dir := t.TempDir()
	sf := store.NewSessionsFile(dir)

	// Write a sessions.json that explicitly has "mode": "" (or no mode
	// field at all). We test both variants.
	type rawEntry struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
		Mode      string    `json:"mode,omitempty"`
	}

	// Variant 1: mode field is absent entirely (omitempty hides empty string).
	raw1 := []rawEntry{
		{ID: "id-a", Name: "no-mode-field", CreatedAt: time.Now().UTC()},
	}
	data1, _ := json.MarshalIndent(raw1, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "sessions.json"), data1, 0o600)

	list, err := sf.Load()
	if err != nil {
		t.Fatalf("Load (no field): %v", err)
	}
	if len(list) != 1 || list[0].Mode != store.ModeShell {
		t.Fatalf("no-field: Mode = %q, want %q", list[0].Mode, store.ModeShell)
	}

	// Variant 2: mode field is explicitly the empty string.
	type rawEntryExplicit struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
		Mode      string    `json:"mode"`
	}
	raw2 := []rawEntryExplicit{
		{ID: "id-b", Name: "empty-mode", CreatedAt: time.Now().UTC(), Mode: ""},
	}
	data2, _ := json.MarshalIndent(raw2, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "sessions.json"), data2, 0o600)

	list2, err := sf.Load()
	if err != nil {
		t.Fatalf("Load (empty string): %v", err)
	}
	if len(list2) != 1 || list2[0].Mode != store.ModeShell {
		t.Fatalf("empty-string: Mode = %q, want %q", list2[0].Mode, store.ModeShell)
	}
}
