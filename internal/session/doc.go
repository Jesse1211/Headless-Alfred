// Package session owns the lifecycle of all concurrent bash sessions
// running in tmux. It is the single source of truth for "which
// sessions exist": all callers (REST handlers, WS handlers, the
// disk-writer goroutine) ask Manager, never tmux directly.
//
// Manager is safe for concurrent use. All methods complete fast (no
// blocking I/O except the underlying tmuxio + store operations).
//
// Ownership:
//   - Manager owns N TmuxShells; each TmuxShell owns one tmux session.
//   - Manager owns sessions.json (via store.SessionsFile).
//   - Manager does NOT own the tmux server process itself — tmux is
//     a daemon started lazily by the first NewSession call.
package session
