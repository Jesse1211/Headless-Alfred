package api

import (
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
	"github.com/jesseliu/headless-alfred/internal/store"
)

func newAdapterTestManager(t *testing.T) *session.Manager {
	t.Helper()
	dir := t.TempDir()
	st, _ := store.New(dir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, err := session.NewManager(session.Config{
		DataDir:      dir,
		Store:        st,
		SessionsFile: store.NewSessionsFile(dir),
		Runner:       tmuxio.NewFakeRunner(),
		Nonce:        "test-nonce",
		MaxSessions:  8,
		Logger:       logger,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestSessionMetaResolver_FindsClaudeUUID(t *testing.T) {
	m := newAdapterTestManager(t)
	meta, err := m.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	// Use the manager's own EnsureClaudeConvoID to populate the uuid
	// instead of poking the store directly — keeps the test honest
	// to the production code path.
	uuid, err := m.EnsureClaudeConvoID(meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	resolver := NewSessionMetaResolver(m)
	got, err := resolver.ClaudeUUIDFor(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != uuid {
		t.Errorf("got %q want %q", got, uuid)
	}
}

func TestSessionMetaResolver_UnknownSessionReturnsErr(t *testing.T) {
	m := newAdapterTestManager(t)
	resolver := NewSessionMetaResolver(m)
	_, err := resolver.ClaudeUUIDFor("nonexistent")
	if !errors.Is(err, ErrUnknownSession) {
		t.Errorf("err: %v, want ErrUnknownSession", err)
	}
}
