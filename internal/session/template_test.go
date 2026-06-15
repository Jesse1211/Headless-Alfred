package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/store"
)

// TestCreate_HasEmptyTemplateID confirms a freshly-minted session has no
// template ID set.
func TestCreate_HasEmptyTemplateID(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.GetTemplateID(meta.ID); got != "" {
		t.Errorf("GetTemplateID = %q, want empty", got)
	}
}

// TestSetTemplateID_GetTemplateID covers the basic set + get round-trip.
func TestSetTemplateID_GetTemplateID(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}

	if err := m.SetTemplateID(meta.ID, "summary-todo"); err != nil {
		t.Fatalf("SetTemplateID: %v", err)
	}
	if got := m.GetTemplateID(meta.ID); got != "summary-todo" {
		t.Errorf("GetTemplateID = %q, want %q", got, "summary-todo")
	}

	// Clearing with empty string also works.
	if err := m.SetTemplateID(meta.ID, ""); err != nil {
		t.Fatalf("SetTemplateID clear: %v", err)
	}
	if got := m.GetTemplateID(meta.ID); got != "" {
		t.Errorf("GetTemplateID after clear = %q, want empty", got)
	}
}

// TestSetTemplateID_PersistsToDisk verifies that SetTemplateID writes the
// new value into sessions.json on disk.
func TestSetTemplateID_PersistsToDisk(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}

	if err := m.SetTemplateID(meta.ID, "summary-todo"); err != nil {
		t.Fatalf("SetTemplateID: %v", err)
	}

	// Read sessions.json directly and assert it contains "summary-todo".
	data, err := os.ReadFile(filepath.Join(m.cfg.DataDir, "sessions.json"))
	if err != nil {
		t.Fatalf("read sessions.json: %v", err)
	}
	if !strings.Contains(string(data), "summary-todo") {
		t.Errorf("sessions.json does not contain %q; content:\n%s", "summary-todo", data)
	}
}

// TestSetTemplateID_UnknownSessionReturnsError ensures the
// ErrSessionNotFound contract holds.
func TestSetTemplateID_UnknownSessionReturnsError(t *testing.T) {
	m, _ := newTestManager(t)
	err := m.SetTemplateID("nope", "summary-todo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestReconcile_ClearsTemplateID verifies that the "stored \ live" branch of
// Reconcile resets template_id to "" (because the template choice is
// entry-time; after a pod restart the user re-picks it).
func TestReconcile_ClearsTemplateID(t *testing.T) {
	m, _ := newTestManager(t)

	// Pre-populate sessions.json with a session that has a template id but
	// whose tmux session does NOT exist (simulates pod restart).
	id := "01HXCA"
	createdAt := time.Now().UTC().Add(-time.Hour)
	_ = m.cfg.SessionsFile.Save([]store.SessionMeta{
		{ID: id, Name: "TemplateWas", CreatedAt: createdAt, TemplateID: "summary-todo"},
	})
	// NOTE: do NOT call fr.NewSession(id, ...) — we want stored \ live.

	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// In-memory template id must be empty.
	if got := m.GetTemplateID(id); got != "" {
		t.Fatalf("GetTemplateID after Reconcile = %q, want empty", got)
	}

	// sessions.json on disk must also have template_id cleared (field
	// omitted or empty).
	persisted, err := m.cfg.SessionsFile.Load()
	if err != nil {
		t.Fatalf("SessionsFile.Load: %v", err)
	}
	var found bool
	for _, sm := range persisted {
		if sm.ID == id {
			found = true
			if sm.TemplateID != "" {
				t.Fatalf("sessions.json template_id = %q, want empty", sm.TemplateID)
			}
		}
	}
	if !found {
		t.Fatalf("session %s not found in persisted list: %+v", id, persisted)
	}
}
