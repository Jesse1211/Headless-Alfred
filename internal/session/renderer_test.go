package session

import (
	"strings"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/store"
)

// TestCreate_HasEmptyRendererAndConvoID confirms a freshly-minted
// session has no renderer or convo UUID until the user enters
// Claude mode.
func TestCreate_HasEmptyRendererAndConvoID(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.GetRenderer(meta.ID); got != "" {
		t.Errorf("GetRenderer = %q, want empty", got)
	}
	all := m.List()
	for _, mt := range all {
		if mt.ID == meta.ID && mt.ClaudeSessionID != "" {
			t.Errorf("freshly-created session has ClaudeSessionID = %q, want empty", mt.ClaudeSessionID)
		}
	}
}

// TestSetRenderer_PersistsAndReadsBack covers the basic round-trip.
func TestSetRenderer_PersistsAndReadsBack(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.ClaudeRenderer{store.RendererUI, store.RendererTUI, ""} {
		if err := m.SetRenderer(meta.ID, r); err != nil {
			t.Fatalf("SetRenderer(%q): %v", r, err)
		}
		if got := m.GetRenderer(meta.ID); got != r {
			t.Errorf("GetRenderer after Set(%q) = %q", r, got)
		}
	}
}

// TestSetRenderer_UnknownSessionReturnsError ensures the
// ErrSessionNotFound contract holds.
func TestSetRenderer_UnknownSessionReturnsError(t *testing.T) {
	m, _ := newTestManager(t)
	err := m.SetRenderer("nope", store.RendererUI)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestEnsureClaudeConvoID_IsIdempotent makes sure repeated calls
// return the same UUID without re-rolling it.
func TestEnsureClaudeConvoID_IsIdempotent(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.EnsureClaudeConvoID(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("first call returned empty UUID")
	}
	// UUID v4 shape: 36 chars, hyphens at the right spots.
	if len(first) != 36 || strings.Count(first, "-") != 4 {
		t.Errorf("UUID shape unexpected: %q", first)
	}
	for i := 0; i < 5; i++ {
		again, err := m.EnsureClaudeConvoID(meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Errorf("call %d returned different UUID: %q vs %q", i, again, first)
		}
	}
}

// TestEnsureClaudeConvoID_UnknownSession returns ErrSessionNotFound.
func TestEnsureClaudeConvoID_UnknownSession(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.EnsureClaudeConvoID("nope"); err == nil {
		t.Fatal("expected error, got nil")
	}
}
