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

func TestManager_Create_AssignsDefaultName(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.Name != "Session 1" {
		t.Fatalf("name = %q, want Session 1", meta.Name)
	}
	if meta.ID == "" {
		t.Fatal("id should be set")
	}
	// And the second one increments.
	meta2, _ := m.Create("")
	if meta2.Name != "Session 2" {
		t.Fatalf("name = %q, want Session 2", meta2.Name)
	}
}

func TestManager_Create_KeepsCustomName(t *testing.T) {
	m, _ := newTestManager(t)
	meta, err := m.Create("training")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.Name != "training" {
		t.Fatalf("name = %q", meta.Name)
	}
}

func TestManager_Create_TrimsAndRejectsEmpty(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("  training  ")
	if meta.Name != "training" {
		t.Fatalf("name = %q, want trimmed", meta.Name)
	}
	// Whitespace-only after trim → falls back to "Session N" rather
	// than erroring, since the user clearly meant "auto-name".
	meta2, err := m.Create("   ")
	if err != nil {
		t.Fatalf("whitespace-only name should auto-name, got error: %v", err)
	}
	if meta2.Name != "Session 2" {
		t.Fatalf("name = %q", meta2.Name)
	}
}

func TestManager_Create_RejectsTooLong(t *testing.T) {
	m, _ := newTestManager(t)
	longName := ""
	for i := 0; i < MaxNameLength+1; i++ {
		longName += "a"
	}
	_, err := m.Create(longName)
	if err != ErrBadName {
		t.Fatalf("expected ErrBadName, got %v", err)
	}
}

func TestManager_Create_EnforcesLimit(t *testing.T) {
	m, _ := newTestManager(t)
	for i := 0; i < 8; i++ {
		_, err := m.Create("")
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	_, err := m.Create("")
	if err != ErrSessionLimit {
		t.Fatalf("expected ErrSessionLimit, got %v", err)
	}
}

func TestManager_Create_PersistsToSessionsFile(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("training")
	sf := m.cfg.SessionsFile
	persisted, _ := sf.Load()
	if len(persisted) != 1 || persisted[0].ID != meta.ID {
		t.Fatalf("sessions.json not persisted: %+v", persisted)
	}
}

func TestManager_Create_CallsTmuxNewSessionAndPipePane(t *testing.T) {
	m, fr := newTestManager(t)
	meta, _ := m.Create("")
	calls := fr.Calls()
	sawNew, sawPipe := false, false
	for _, c := range calls {
		if c.Method == "NewSession" && c.Args[0] == meta.ID {
			sawNew = true
		}
		if c.Method == "PipePane" && c.Args[0] == meta.ID && c.Args[1] != "" {
			sawPipe = true
		}
	}
	if !sawNew || !sawPipe {
		t.Fatalf("Create did not start tmux session+pipe: %+v", calls)
	}
}
