package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/claudehistory"
	"github.com/jesseliu/headless-alfred/internal/claudestate"
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

	// RecapUpdates receives a date string each time a recap file is
	// written. WS handler subscribes via recapBroadcaster to fan out to
	// every connected client. Nil-safe — broadcaster degrades to no-op
	// when source is nil.
	RecapUpdates <-chan string

	// PVCLimitBytes is the configured PVC quota, parsed from the
	// ALFRED_PVC_LIMIT env var (Helm sets it from
	// .Values.persistence.size). 0 = unknown; the disk-pressure
	// banner then shows "X.X GiB used (unknown limit)" instead of
	// a percentage, and the threshold-based alert is suppressed.
	PVCLimitBytes uint64

	// ClaudeStateManager is the process-singleton state registry
	// constructed in cmd/alfred-server/main.go (Task 8). The router
	// shares it across the HTTP claude-state handler and the WS
	// inbound event router.
	ClaudeStateManager *claudestate.SessionManager
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RecoverMiddleware())
	r.Use(RequestLogger())

	r.Get("/healthz", HealthzHandler().ServeHTTP)
	r.Get("/readyz", ReadyzHandler(d.Ready).ServeHTTP)
	r.Post("/api/login", LoginHandler(d.Auth, d.RateLimiter).ServeHTTP)
	broadcaster := newRecapBroadcaster(d.RecapUpdates)
	// Disk usage poller. 60s tick is plenty — writes that move the
	// gauge meaningfully (a Claude jsonl growing, summaries being
	// rewritten) accumulate slowly, and the alert thresholds are
	// coarse (80% / 95%). Could be wired off via Deps later if needed.
	disk := newDiskBroadcaster(d.Manager.DataDir(), d.PVCLimitBytes, 60*time.Second)
	r.Get("/ws", WSHandler(d.Manager, d.Auth, d.Bridge, d.Dispatcher, broadcaster, disk).ServeHTTP)

	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(d.Auth))

		// Session CRUD.
		r.Get("/api/sessions", ListSessionsHandler(d.Manager).ServeHTTP)
		r.Get("/api/sessions/{id}", GetSessionHandler(d.Manager).ServeHTTP)
		r.Post("/api/sessions", CreateSessionHandler(d.Manager).ServeHTTP)
		r.Patch("/api/sessions/{id}", RenameSessionHandler(d.Manager).ServeHTTP)
		r.Delete("/api/sessions/{id}", DeleteSessionHandler(d.Manager).ServeHTTP)

		// Session-scoped commands.
		r.Get("/api/sessions/{sid}/commands", ListCommandsHandler(d.Manager).ServeHTTP)
		r.Get("/api/sessions/{sid}/commands/{id}", GetCommandHandler(d.Manager).ServeHTTP)
		r.Post("/api/sessions/{sid}/commands/{id}/stop", StopCommandHandler(d.Manager).ServeHTTP)

		// Templates.
		r.Get("/api/templates", ListTemplatesHandler().ServeHTTP)
		r.Get("/api/templates/{id}", GetTemplateHandler().ServeHTTP)

		// Summary.
		r.Get("/api/sessions/{sid}/summary", GetSummaryHandler(d.Manager.DataDir()).ServeHTTP)

		// Notes.
		r.Get("/api/sessions/{sid}/note", GetNoteHandler(d.Manager.DataDir()).ServeHTTP)
		r.Put("/api/sessions/{sid}/note", PutNoteHandler(d.Manager.DataDir()).ServeHTTP)

		// Claude UI chat history (rebuilt from CLI jsonl).
		r.Get("/api/sessions/{sid}/claude-history",
			GetClaudeHistoryHandler(d.Manager, claudehistory.NewLocator()).ServeHTTP)

		// Claude UI chat state (server-authoritative; persisted snapshot
		// + jsonl merge). Replaces /claude-history.
		r.Get("/api/sessions/{sid}/claude-state",
			GetClaudeStateHandler(
				d.ClaudeStateManager,
				NewSessionMetaResolver(d.Manager),
			).ServeHTTP)

		// Recap (file content).
		r.Get("/api/recaps", ListRecapsHandler(d.Manager.DataDir()).ServeHTTP)
		r.Get("/api/recaps/{date}", GetRecapHandler(d.Manager.DataDir()).ServeHTTP)

		// Recap session (lifecycle).
		r.Post("/api/recap-sessions", CreateRecapSessionHandler(d.Manager).ServeHTTP)
		r.Delete("/api/recap-sessions/current", DeleteRecapSessionHandler(d.Manager).ServeHTTP)

		// Git credentials (writes ~/.git-credentials so git clone/pull/push
		// don't need the token on the command line and don't prompt).
		r.Post("/api/git-credentials", GitCredentialsHandler().ServeHTTP)

		// Anthropic OAuth credentials (writes ~/.claude/.credentials.json
		// so the `claude` CLI in any session can authenticate without
		// going through its own /login flow — which can't complete
		// inside the chat UI).
		r.Post("/api/anthropic-credentials", AnthropicCredentialsHandler().ServeHTTP)

		// Disk usage of the PVC. Computed by walking /data + ~/ and
		// comparing against the PVCLimitBytes from Helm; statfs would
		// only see the underlying node disk (100GB on oracle), which
		// gives a misleading reading vs the 5Gi quota writes actually
		// fail against.
		r.Get("/api/disk-usage", DiskUsageHandler(d.Manager.DataDir(), d.PVCLimitBytes).ServeHTTP)

		// Claude CLI version probe + runtime upgrade. The upgrade
		// endpoint streams npm output back as chunked text/plain so
		// the user sees progress (npm runs can take 10s+).
		r.Get("/api/claude-cli/version", ClaudeCLIVersionHandler().ServeHTTP)
		r.Post("/api/claude-cli/upgrade", ClaudeCLIUpgradeHandler().ServeHTTP)
	})

	r.NotFound(static.Handler().ServeHTTP)
	return r
}
