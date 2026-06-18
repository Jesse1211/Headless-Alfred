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
	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/claudehistory"
	"github.com/jesseliu/headless-alfred/internal/claudestate"
	"github.com/jesseliu/headless-alfred/internal/recap"
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
	// PVC quota for the disk-pressure banner. Helm sets this from
	// .Values.persistence.size (e.g. "5Gi"). Empty/unparseable → 0,
	// which makes the banner show "unknown" and skip percentage
	// alerts; the size is informational rather than load-bearing,
	// so we don't fail boot on a bad value.
	pvcLimitRaw := envOr("ALFRED_PVC_LIMIT", "")
	pvcLimit := api.ParsePVCLimit(pvcLimitRaw)
	if pvcLimitRaw != "" && pvcLimit == 0 {
		logger.Warn("ALFRED_PVC_LIMIT could not be parsed; disk percent will read 0", "raw", pvcLimitRaw)
	}

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

	// Sentinel nonce, persisted to disk so it survives alfred restarts.
	// In-flight sentinels emitted by a previous alfred would otherwise be
	// unrecognized by the new alfred's parser (Go-restart resume would never
	// see EventEnd, leaving the on-disk record stuck at "running" forever).
	noncePath := filepath.Join(dataDir, "nonce")
	var nonce string
	if b, err := os.ReadFile(noncePath); err == nil && len(b) > 0 {
		nonce = string(b)
	} else {
		var n [8]byte
		if _, err := rand.Read(n[:]); err != nil {
			logger.Error("nonce", "err", err)
			os.Exit(2)
		}
		nonce = hex.EncodeToString(n[:])
		if err := os.WriteFile(noncePath, []byte(nonce), 0600); err != nil {
			logger.Error("persist nonce", "err", err)
			os.Exit(2)
		}
	}

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

	// PreToolUse hook bridge. Bound to 127.0.0.1 only; the in-pod
	// hook script connects to it whenever Claude wants to call a
	// tool. The bridge blocks until ws.go calls Resolve(), wiring
	// the user's Allow/Deny back into Claude.
	//
	// Routing: bridge.onAsk receives requests keyed by Claude's
	// session_id. Dispatcher.OnAsk translates that into the matching
	// Alfred sessionID and forwards to whichever WS client is
	// subscribed (one per session at a time).
	//
	// Two auto paths:
	//   - autoAllow: the convo is a foreign claude (someone running
	//     `claude` in another terminal while Alfred is up). Allow so
	//     we don't globally break their tools.
	//   - autoDeny:  the convo is one of ours but no UI tab is
	//     connected. Deny — the user opted into ask-before-each.
	const bridgePort = 8090
	dispatcher := claude.NewDispatcher()
	var bridge *claude.Bridge
	bridge = claude.NewBridge(dispatcher.OnAsk(
		mgr.FindByClaudeConvoID,
		func(alfredSID string) bool {
			meta, ok := mgr.FindByID(alfredSID)
			return ok && meta.Kind == store.KindRecap
		},
		// isBypassSession: when the user picked "bypass permissions"
		// in the StartClaudeDialog, the dispatcher auto-allows every
		// tool call (except AskUserQuestion). Matches the user's
		// "don't interrupt me" intent — without this, --dangerously-
		// skip-permissions on the CLI only silenced the CLI prompt
		// while we kept popping our own Allow/Deny cards.
		mgr.GetClaudeBypass,
		func(toolUseID string) {
			bridge.Resolve(toolUseID, claude.Decision{Permission: "allow"})
		},
		func(toolUseID, reason string) {
			bridge.Resolve(toolUseID, claude.Decision{
				Permission: "deny",
				Reason:     reason,
			})
		},
		mgr.DataDir(),
	))
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()
	if err := bridge.Start(bridgeCtx, bridgePort); err != nil {
		logger.Error("claude bridge listen", "port", bridgePort, "err", err)
		os.Exit(2)
	}
	logger.Info("claude bridge listening", "addr", bridge.Addr())

	rl := auth.NewRateLimiter(5, time.Minute)

	// Recap-file watcher: emits date strings when a recaps/<date>.md
	// file is written. Broadcaster in api package fans out to all WS
	// clients. Failure is non-fatal — the rest of the server runs fine
	// without it (recap UI becomes stale but still usable).
	recapUpdates := make(chan string, 16)
	recapWatcher, recapWatchErr := recap.StartWatcher(dataDir, func(date string) {
		select {
		case recapUpdates <- date:
		default:
			logger.Warn("recapUpdates channel full; dropping", "date", date)
		}
	})
	if recapWatchErr != nil {
		logger.Warn("recap watcher startup failed; recap UI will be stale", "err", recapWatchErr)
	} else {
		defer recapWatcher.Stop()
	}

	csMgr := claudestate.NewSessionManager(
		dataDir,
		api.NewJsonlLocatorAdapter(claudehistory.NewLocator()),
	)
	defer func() {
		if err := csMgr.Shutdown(context.Background()); err != nil {
			logger.Error("claudestate shutdown", "err", err)
		}
	}()

	router := api.NewRouter(api.Deps{
		Manager:            mgr,
		Auth:               a,
		RateLimiter:        rl,
		Ready:              ready.Load,
		Bridge:             bridge,
		Dispatcher:         dispatcher,
		RecapUpdates:       recapUpdates,
		PVCLimitBytes:      pvcLimit,
		ClaudeStateManager: csMgr,
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
