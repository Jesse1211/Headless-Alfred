package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/claudehistory"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/static"
)

// Deps is the runtime dependency bundle. Plan 7 fills these in main.go.
type Deps struct {
	Manager     *session.Manager
	Auth        auth.Auth
	RateLimiter *auth.RateLimiter
	Ready       func() bool

	// Bridge is the localhost HTTP listener that backs the PreToolUse
	// hook (see internal/claude/bridge.go). The WS handler uses it to
	// unblock pending tool-approval HTTP requests when a tool_decision
	// frame arrives from the client. Can be nil when Claude UI is
	// disabled; the WS handler degrades gracefully.
	Bridge *claude.Bridge

	// Dispatcher fans tool-approval requests from the bridge out to
	// the appropriate WS client per Alfred session. Always paired
	// with Bridge.
	Dispatcher *claude.Dispatcher
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RecoverMiddleware())
	r.Use(RequestLogger())

	r.Get("/healthz", HealthzHandler().ServeHTTP)
	r.Get("/readyz", ReadyzHandler(d.Ready).ServeHTTP)
	r.Post("/api/login", LoginHandler(d.Auth, d.RateLimiter).ServeHTTP)
	r.Get("/ws", WSHandler(d.Manager, d.Auth, d.Bridge, d.Dispatcher).ServeHTTP)

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

		// Templates.
		r.Get("/api/templates/{id}", GetTemplateHandler().ServeHTTP)

		// Summary.
		r.Get("/api/sessions/{sid}/summary", GetSummaryHandler(d.Manager.DataDir()).ServeHTTP)

		// Claude UI chat history (rebuilt from CLI jsonl).
		r.Get("/api/sessions/{sid}/claude-history",
			GetClaudeHistoryHandler(d.Manager, claudehistory.NewLocator()).ServeHTTP)

		// Git credentials (writes ~/.git-credentials so git clone/pull/push
		// don't need the token on the command line and don't prompt).
		r.Post("/api/git-credentials", GitCredentialsHandler().ServeHTTP)

		// Anthropic OAuth credentials (writes ~/.claude/.credentials.json
		// so the `claude` CLI in any session can authenticate without
		// going through its own /login flow — which can't complete
		// inside the chat UI).
		r.Post("/api/anthropic-credentials", AnthropicCredentialsHandler().ServeHTTP)
	})

	r.NotFound(static.Handler().ServeHTTP)
	return r
}
