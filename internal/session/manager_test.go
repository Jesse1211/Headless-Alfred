package session

import (
	"log/slog"
	"os"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
	"github.com/jesseliu/headless-alfred/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *tmuxio.FakeRunner) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	fr := tmuxio.NewFakeRunner()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, err := NewManager(Config{
		DataDir:      dir,
		Store:        st,
		SessionsFile: store.NewSessionsFile(dir),
		Runner:       fr,
		Nonce:        "test-nonce",
		MaxSessions:  8,
		Logger:       logger,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, fr
}

func TestManager_EmptyOnFreshConstruction(t *testing.T) {
	m, _ := newTestManager(t)
	list := m.List()
	if len(list) != 0 {
		t.Fatalf("fresh Manager should list zero sessions, got %+v", list)
	}
}

func TestManager_RejectsMissingConfig(t *testing.T) {
	_, err := NewManager(Config{})
	if err == nil {
		t.Fatal("NewManager with empty config should error")
	}
}
