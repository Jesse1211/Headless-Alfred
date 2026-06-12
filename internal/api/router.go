package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/static"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// Deps bundles the runtime dependencies the router needs to mount handlers.
// main.go constructs these once and passes them to NewRouter.
type Deps struct {
	Shell       *shell.Shell
	Store       *store.Store
	Auth        auth.Auth
	RateLimiter *auth.RateLimiter
	Ready       func() bool
}

// NewRouter assembles the full HTTP surface:
//   - Health probes (public)
//   - /api/login (public, rate-limited)
//   - /ws (public — auth checked in upgrade)
//   - /api/commands/* (authenticated)
//   - Static SPA fallback for everything else
//
// Middleware order matters: panic recovery is outermost so that recovered
// panics in any handler still produce a clean 500. Request logging runs
// next so even panic logs include the request line.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RecoverMiddleware())
	r.Use(RequestLogger())

	// Public routes.
	r.Get("/healthz", HealthzHandler().ServeHTTP)
	r.Get("/readyz", ReadyzHandler(d.Ready).ServeHTTP)
	r.Post("/api/login", LoginHandler(d.Auth, d.RateLimiter).ServeHTTP)
	r.Get("/ws", WSHandler(d.Shell, d.Store, d.Auth).ServeHTTP)

	// Authenticated REST.
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(d.Auth))
		r.Get("/api/commands", ListCommandsHandler(nil).ServeHTTP)
		r.Get("/api/commands/{id}", GetCommandHandler(nil).ServeHTTP)
		r.Post("/api/commands/{id}/stop", StopCommandHandler(nil).ServeHTTP)
	})

	// Static (lowest priority — only hit if nothing above matched).
	r.NotFound(static.Handler().ServeHTTP)

	return r
}
