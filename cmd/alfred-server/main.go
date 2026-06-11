// Command alfred-server runs the Headless Alfred backend: a single Go
// process holding the bash session, persisting per-command output, and
// serving HTTP/WebSocket to the React UI.
//
// Configuration is entirely via environment variables. See deploy/manifests/
// for the K8s wiring.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jesseliu/headless-alfred/internal/api"
	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	addr := envOr("ALFRED_ADDR", ":8080")
	dataDir := envOr("ALFRED_DATA_DIR", "/data")

	// Auth must be fully configured at startup. Refuse to boot otherwise —
	// a misconfigured Pod with an empty token equals an open shell on the
	// public internet.
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
	// Any records left in "running" from a previous process were owned by
	// a bash that's now gone. Re-classify them so the UI doesn't show them
	// as alive.
	if err := st.SweepRunningToInterrupted(); err != nil {
		logger.Error("sweep", "err", err)
	}

	sh := shell.NewShell(logger)
	if err := sh.Start(); err != nil {
		logger.Error("shell start", "err", err)
		os.Exit(2)
	}

	// Once the shell and store are up, we mark ourselves ready.
	var ready atomic.Bool
	ready.Store(true)

	// 5 login attempts per minute per IP. See design.md §10.
	rl := auth.NewRateLimiter(5, time.Minute)

	router := api.NewRouter(api.Deps{
		Shell:       sh,
		Store:       st,
		Auth:        a,
		RateLimiter: rl,
		Ready:       ready.Load,
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Run the server in a goroutine so main can wait on signals.
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

	// Wait for SIGINT/SIGTERM or a listener failure.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sigs:
		logger.Info("signal received", "signal", s.String())
	case err := <-serveErr:
		if err != nil {
			logger.Error("listener failed", "err", err)
			os.Exit(1)
		}
	}

	logger.Info("shutting down")
	ready.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	_ = sh.Close()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
