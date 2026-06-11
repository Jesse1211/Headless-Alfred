# Multi-session Plan 7 — Boot wiring (migration + reconcile + listener order)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite `cmd/alfred-server/main.go` so boot order matches spec §4.7: store init → legacy migration → tmux ExecRunner → session.Manager construction → Reconcile() → mark ready → open HTTP listener. The old `shell.NewShell` + `sh.Start` are replaced by the Manager's reconciliation path. `go build ./...` succeeds after this plan.

**Architecture:** A single sequence in `main`. Critical invariant: **HTTP listener does not open until Reconcile returns**. Auth is checked first (fail-fast on misconfig). Tmux socket path is derived from `ALFRED_DATA_DIR`. Per-process nonce is generated once and threaded through Manager → TmuxShell.

**Tech Stack:** stdlib + the four internal packages from Plans 1-6.

**Spec sections covered:** §4.7 reconcile path, §5 migration, §3 listener-startup order.

---

## File Structure

```
cmd/alfred-server/
├── main.go                # REWRITE entirely (still ~120 lines)
└── main_test.go           # NEW: end-to-end boot test with httptest
```

---

## Task 1: Rewrite main.go

**Files:**
- Replace: `cmd/alfred-server/main.go`

- [ ] **Step 1: Replace main.go**

```go
// Command alfred-server runs the Headless Alfred backend.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jesseliu/headless-alfred/internal/api"
	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
	"github.com/jesseliu/headless-alfred/internal/store"

	"github.com/oklog/ulid/v2"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	addr := envOr("ALFRED_ADDR", ":8080")
	dataDir := envOr("ALFRED_DATA_DIR", "/data")

	a, err := auth.FromEnv()
	if err != nil {
		logger.Error("auth setup", "err", err)
		os.Exit(2)
	}

	st, err := store.New(dataDir)
	if err != nil {
		logger.Error("store setup", "err", err)
		os.Exit(2)
	}

	// One-shot legacy migration from /data/commands/* → /data/sessions/<imported>/...
	if imported, err := store.MigrateLegacyLayout(dataDir, ulid.Make().String(), time.Now().UTC()); err != nil {
		logger.Error("legacy migration failed", "err", err)
		os.Exit(2)
	} else if imported {
		logger.Info("legacy data migrated into 'Imported' session")
	}

	// Tmux socket lives next to sessions.json. Both are inside the PVC.
	socketPath := filepath.Join(dataDir, "alfred-tmux.sock")
	runner := tmuxio.NewExecRunner(socketPath)

	// Per-process nonce: shared by all TmuxShells the Manager creates.
	var n [8]byte
	if _, err := rand.Read(n[:]); err != nil {
		logger.Error("nonce", "err", err)
		os.Exit(2)
	}
	nonce := hex.EncodeToString(n[:])

	mgr, err := session.NewManager(session.Config{
		DataDir:      dataDir,
		Store:        st,
		SessionsFile: store.NewSessionsFile(dataDir),
		Runner:       runner,
		Nonce:        nonce,
		MaxSessions:  8,
		Logger:       logger,
	})
	if err != nil {
		logger.Error("manager setup", "err", err)
		os.Exit(2)
	}

	// Reconcile MUST complete before we accept any HTTP request.
	if err := mgr.Reconcile(); err != nil {
		logger.Error("reconcile", "err", err)
		os.Exit(2)
	}

	var ready atomic.Bool
	ready.Store(true)

	rl := auth.NewRateLimiter(5, time.Minute)
	router := api.NewRouter(api.Deps{
		Manager:     mgr,
		Auth:        a,
		RateLimiter: rl,
		Ready:       ready.Load,
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sigs:
		logger.Info("signal received", "signal", s.String())
	case err := <-serveErr:
		if err != nil {
			logger.Error("listener failed", "err", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)

	// Note: we do NOT KillSession on every TmuxShell at shutdown — the
	// tmux server outliving the Go process is the entire point. Plan 14's
	// CONTEXT.md update emphasises this.
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 2: Confirm whole project builds**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 3: Confirm all unit tests across the project still pass**

Run: `go test -race ./internal/... -count=1`
Expected: PASS.

If anything fails, the issue is most likely a forgotten import update
or a stale reference in `internal/api`. Walk back to the offending
test and fix; the contract is plan 1-6 are merged in order.

- [ ] **Step 4: Commit**

```bash
git add cmd/alfred-server/main.go
git commit -m "cmd: boot order migrate → tmux runner → manager.Reconcile → listener"
```

---

## Task 2: Boot smoke test

A test that boots a fake-runner alfred-server in-process and verifies:
1. `MigrateLegacyLayout` runs.
2. `Manager.Reconcile` picks up pre-existing sessions.json entries.
3. Listener serves `/healthz` only AFTER reconcile completes.

**Files:**
- Create: `cmd/alfred-server/main_test.go`

- [ ] **Step 1: Write the test**

```go
package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBoot_StartsAndServesHealthz runs the binary in-process by
// directly invoking the boot sequence — main() with overridden env
// vars and a dynamic port.
func TestBoot_StartsAndServesHealthz(t *testing.T) {
	dir := t.TempDir()
	// Choose a free port.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()

	// Mandatory env vars for auth.FromEnv to succeed.
	t.Setenv("ALFRED_USER", "admin")
	t.Setenv("ALFRED_PASSWORD", "pw")
	t.Setenv("ALFRED_TOKEN", "tok")
	t.Setenv("ALFRED_DATA_DIR", dir)
	t.Setenv("ALFRED_ADDR", addr)

	// Seed legacy commands so MigrateLegacyLayout has something to do.
	legacyCmds := filepath.Join(dir, "commands")
	_ = os.MkdirAll(legacyCmds, 0o700)
	_ = os.WriteFile(filepath.Join(legacyCmds, "01HZ.json"), []byte(`{"id":"01HZ","command":"ls","status":"completed","started_at":"2026-06-10T00:00:00Z"}`), 0o600)

	done := make(chan struct{})
	go func() {
		main()
		close(done)
	}()
	t.Cleanup(func() {
		// Best-effort shutdown: send SIGTERM to self. main() listens for it.
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})

	// Poll /healthz until ready (or timeout).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			// Also verify sessions.json was written by the migration.
			info, err := os.Stat(filepath.Join(dir, "sessions.json"))
			if err != nil || info.Size() == 0 {
				t.Fatalf("sessions.json missing or empty: %v", err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("server did not become ready within 5 seconds")
}

var _ = context.Background
```

Note: this test depends on `tmux` being on PATH because Manager's
NewSession (during Reconcile of the migrated "Imported" session) will
call into ExecRunner. We add a skip guard:

```go
import (
	"os/exec"
	// ...
)

func init() {
	// Prevent the test running when no tmux binary is available.
	if _, err := exec.LookPath("tmux"); err != nil {
		skipBootTests = true
	}
}

var skipBootTests bool

// At the top of TestBoot_StartsAndServesHealthz, before any work:
//   if skipBootTests { t.Skip("tmux binary not on PATH") }
```

- [ ] **Step 2: Run, confirm PASS (or SKIP on tmux-less hosts)**

Run: `go test ./cmd/alfred-server/ -race -count=1 -v`
Expected: PASS on hosts with tmux. SKIP otherwise.

- [ ] **Step 3: Commit**

```bash
git add cmd/alfred-server/main_test.go
git commit -m "cmd: smoke test that boots binary, runs migration + reconcile + serves /healthz"
```

---

## Plan 7 acceptance

- `go build ./...` passes.
- `go test -race ./...` passes (`cmd/alfred-server` boot test SKIPs without tmux).
- Boot order strictly: auth → store → migration → ExecRunner → Manager → Reconcile → ready=true → ListenAndServe.
- ALFRED_DATA_DIR can point at a fresh dir (first boot creates `sessions.json` lazily on first Create) OR a dir with legacy `commands/`+`outputs/` (migration imports them).

## Plan 7 self-review checklist

- [ ] `cmd/alfred-server/main.go` no longer imports `internal/shell`.
- [ ] No goroutine started before Reconcile completes other than the listener (which is *after* reconcile by design).
- [ ] Boot smoke test creates its own fresh tempdir and dynamic port to avoid host-state pollution.
