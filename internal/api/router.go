package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/static"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// Deps is the runtime dependency bundle. Plan 7 fills these in main.go.
type Deps struct {
	Manager     *session.Manager
	Auth        auth.Auth
	RateLimiter *auth.RateLimiter
	Ready       func() bool

	// Shell and Store are retained so the legacy WS handler still
	// compiles in this plan. Plan 6 rewrites ws.go to use Manager
	// and drops these fields.
	Shell *shell.Shell
	Store *store.Store
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RecoverMiddleware())
	r.Use(RequestLogger())

	r.Get("/healthz", HealthzHandler().ServeHTTP)
	r.Get("/readyz", ReadyzHandler(d.Ready).ServeHTTP)
	r.Post("/api/login", LoginHandler(d.Auth, d.RateLimiter).ServeHTTP)
	r.Get("/ws", WSHandler(d.Shell, d.Store, d.Auth).ServeHTTP)

	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(d.Auth))

		// Session CRUD.
		r.Get("/api/sessions", ListSessionsHandler(d.Manager).ServeHTTP)
		r.Post("/api/sessions", CreateSessionHandler(d.Manager).ServeHTTP)
		r.Patch("/api/sessions/{id}", RenameSessionHandler(d.Manager).ServeHTTP)
		r.Delete("/api/sessions/{id}", DeleteSessionHandler(d.Manager).ServeHTTP)

		// Session-scoped commands.
		r.Get("/api/sessions/{sid}/commands", ListCommandsHandler(d.Manager).ServeHTTP)
		r.Get("/api/sessions/{sid}/commands/{id}", GetCommandHandler(d.Manager).ServeHTTP)
		r.Post("/api/sessions/{sid}/commands/{id}/stop", StopCommandHandler(d.Manager).ServeHTTP)
	})

	r.NotFound(static.Handler().ServeHTTP)
	return r
}
