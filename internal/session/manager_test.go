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

func TestManager_Rename_UpdatesAndPersists(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("Session 1")
	if err := m.Rename(meta.ID, "training"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	list := m.List()
	if list[0].Name != "training" {
		t.Fatalf("name not updated: %+v", list[0])
	}
	persisted, _ := m.cfg.SessionsFile.Load()
	if persisted[0].Name != "training" {
		t.Fatalf("not persisted: %+v", persisted)
	}
}

func TestManager_Rename_RejectsEmptyAndTooLong(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("Session 1")
	if err := m.Rename(meta.ID, "   "); err != ErrBadName {
		t.Fatalf("empty: expected ErrBadName, got %v", err)
	}
	long := ""
	for i := 0; i < MaxNameLength+1; i++ {
		long += "x"
	}
	if err := m.Rename(meta.ID, long); err != ErrBadName {
		t.Fatalf("too-long: expected ErrBadName, got %v", err)
	}
}

func TestManager_Rename_UnknownIDReturnsNotFound(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Rename("nope", "x"); err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestManager_Rename_FiresListener(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("Session 1")
	called := make(chan struct{}, 1)
	m.AddRenameListener(func(id, name string) {
		if id == meta.ID && name == "training" {
			called <- struct{}{}
		}
	})
	_ = m.Rename(meta.ID, "training")
	select {
	case <-called:
	default:
		t.Fatal("listener not called")
	}
}

func TestManager_Close_RemovesFromListAndDeletesStoreDir(t *testing.T) {
	m, fr := newTestManager(t)
	meta, _ := m.Create("Session 1")

	// Place a marker file in the store dir to prove RemoveAll works.
	_ = m.cfg.Store.WriteOutput(meta.ID, "marker", []byte("x"))

	if err := m.Close(meta.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("after Close list should be empty: %+v", m.List())
	}
	// Tmux session killed.
	calls := fr.Calls()
	sawKill := false
	for _, c := range calls {
		if c.Method == "KillSession" && c.Args[0] == meta.ID {
			sawKill = true
		}
	}
	if !sawKill {
		t.Fatalf("KillSession not called: %+v", calls)
	}
	// Store dir gone.
	if _, err := os.Stat(m.cfg.Store.SessionDir(meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("store dir not removed: %v", err)
	}
	// sessions.json no longer mentions it.
	persisted, _ := m.cfg.SessionsFile.Load()
	if len(persisted) != 0 {
		t.Fatalf("sessions.json not updated: %+v", persisted)
	}
}

func TestManager_Close_UnknownIDReturnsNotFound(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Close("nope"); err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestManager_Close_FiresListener(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("")
	called := make(chan string, 1)
	m.AddCloseListener(func(id string) {
		called <- id
	})
	_ = m.Close(meta.ID)
	select {
	case got := <-called:
		if got != meta.ID {
			t.Fatalf("listener got %q, want %q", got, meta.ID)
		}
	default:
		t.Fatal("listener not called")
	}
}

// Regression: multiple WS connections each Add their own listener;
// Close must call EVERY registered listener, not just the last one.
func TestManager_Close_FiresAllListeners_MultiSubscriber(t *testing.T) {
	m, _ := newTestManager(t)
	meta, _ := m.Create("")
	got := make(chan int, 3)
	m.AddCloseListener(func(string) { got <- 1 })
	m.AddCloseListener(func(string) { got <- 2 })
	m.AddCloseListener(func(string) { got <- 3 })
	_ = m.Close(meta.ID)
	seen := map[int]bool{}
	for i := 0; i < 3; i++ {
		select {
		case v := <-got:
			seen[v] = true
		default:
			t.Fatalf("only %d/3 listeners fired", i)
		}
	}
	if !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("missed listener: %+v", seen)
	}
}

func TestManager_AddCloseListener_RemoveStopsCallbacks(t *testing.T) {
	m, _ := newTestManager(t)
	meta1, _ := m.Create("")
	called := 0
	remove := m.AddCloseListener(func(string) { called++ })
	_ = m.Close(meta1.ID)
	if called != 1 {
		t.Fatalf("after first Close, called=%d, want 1", called)
	}
	meta2, _ := m.Create("")
	remove()
	_ = m.Close(meta2.ID)
	if called != 1 {
		t.Fatalf("listener fired after remove(): called=%d", called)
	}
}
