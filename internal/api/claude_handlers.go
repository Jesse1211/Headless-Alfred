package api

// Claude-UI-specific WS plumbing — handlers for the claude_prompt /
// tool_decision / enter_claude / exit_claude / stdin frames, plus
// the per-WS-connection state map of in-flight `claude -p` runs.
//
// ws.go owns the connection loop and the shell-mode (run/started/
// chunk/done) handler; it dispatches into the handlers here for any
// frame that touches Claude.

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/recap"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
	"github.com/jesseliu/headless-alfred/internal/summary"
	"github.com/jesseliu/headless-alfred/internal/template"
)

// composePromptText assembles the final prompt text that will be
// piped to `claude -p` stdin. If the session has a template
// active, the rendered template is appended after the user's
// message separated by a markdown horizontal rule so Claude can
// tell what came from the user and what came from the harness.
func composePromptText(userText, templateID, sessionID, summaryPath string) string {
	rendered := template.Render(templateID, template.RenderArgs{
		SessionID:   sessionID,
		SummaryPath: summaryPath,
	})
	if rendered == "" {
		return userText
	}
	return userText + "\n\n---\n" + rendered
}

// claudeRunState tracks one in-flight `claude -p` invocation per
// alfred session. Stored in a per-WS-connection map; lifetime ends
// when the runner exits or the user clicks Stop.
type claudeRunState struct {
	cancel context.CancelFunc
	stop   func()
}

// claudeRunStateMap is a tiny mutex-guarded map of in-flight runs.
// The reaper goroutine for each prompt deletes from this map after
// pr.Wait() returns, while the main goroutine reads/writes it from
// handleInbound. Without the mutex this is a data race that Go's
// race detector flags and that can crash with "concurrent map
// writes" in production.
type claudeRunStateMap struct {
	mu sync.Mutex
	m  map[string]*claudeRunState
}

func newClaudeRunStateMap() *claudeRunStateMap {
	return &claudeRunStateMap{m: map[string]*claudeRunState{}}
}

func (s *claudeRunStateMap) get(sid string) (*claudeRunState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.m[sid]
	return st, ok
}

func (s *claudeRunStateMap) set(sid string, st *claudeRunState) {
	s.mu.Lock()
	s.m[sid] = st
	s.mu.Unlock()
}

// take atomically removes and returns the state for sid, so callers
// can decide whether to stop / cancel it without racing another
// remover. Returns (nil, false) if sid wasn't present.
func (s *claudeRunStateMap) take(sid string) (*claudeRunState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.m[sid]
	if ok {
		delete(s.m, sid)
	}
	return st, ok
}

// takeAll removes and returns every state. Used on WS disconnect
// to stop in-flight runners.
func (s *claudeRunStateMap) takeAll() []*claudeRunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*claudeRunState, 0, len(s.m))
	for _, st := range s.m {
		out = append(out, st)
	}
	s.m = map[string]*claudeRunState{}
	return out
}

// stopRun cleanly tears down one in-flight runner state: send SIGINT
// to the child via Stop, then cancel its context so any blocked
// goroutines (the prompt-forwarder, the reaper) wake up.
func stopRun(st *claudeRunState) {
	if st == nil {
		return
	}
	if st.stop != nil {
		st.stop()
	}
	if st.cancel != nil {
		st.cancel()
	}
}

// claudeEventEnvelope carries one parsed claude stream-json event from
// a per-session runner goroutine to the WS write loop.
type claudeEventEnvelope struct {
	sessionID string
	kind      claude.EventKind
	payload   any
}

// forwardAsks pumps PendingRequests from the dispatcher's per-session
// channel onto the per-WS shared asks channel.
func forwardAsks(in <-chan claude.PendingRequest, out chan<- claude.PendingRequest, stop <-chan struct{}) {
	for {
		select {
		case req, ok := <-in:
			if !ok {
				return
			}
			select {
			case out <- req:
			case <-stop:
				return
			}
		case <-stop:
			return
		}
	}
}

// claudeEventPayload extracts the concrete variant payload from an
// Event for JSON marshalling. Returns nil for variants with no
// payload (e.g. MessageStop).
func claudeEventPayload(ev claude.Event) any {
	switch ev.Kind {
	case claude.KindSystem:
		return ev.System
	case claude.KindRateLimit:
		return ev.RateLimit
	case claude.KindTextDelta:
		return ev.TextDelta
	case claude.KindTextBlockEnd:
		return ev.TextBlockEnd
	case claude.KindToolUseStart:
		return ev.ToolUseStart
	case claude.KindToolUseEnd:
		return ev.ToolUseEnd
	case claude.KindToolResult:
		return ev.ToolResult
	case claude.KindMessageStart:
		return ev.MessageStart
	case claude.KindMessageDelta:
		return ev.MessageDelta
	case claude.KindMessageStop:
		return nil
	case claude.KindResult:
		return ev.Result
	case claude.KindUnknown:
		return ev.Unknown
	}
	return nil
}

func handleEnterClaude(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if !requireSessionID(msg, "enter_claude", write) {
		return
	}
	// Renderer selects between V0 TUI (xterm.js + raw PTY passthrough)
	// and V1 UI (React chat + claude -p stream-json). Empty defaults to
	// "tui" for backward compat with V0 clients that don't send the
	// field. New clients always send it.
	renderer := store.ClaudeRenderer(msg.Renderer)
	if renderer == "" {
		renderer = store.RendererTUI
	}
	if renderer != store.RendererTUI && renderer != store.RendererUI {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "renderer must be 'tui' or 'ui'"})
		return
	}
	if m.GetMode(msg.SessionID) == store.ModeClaude {
		// Idempotent: if the session is already in claude mode with the
		// same renderer (e.g. the user clicked Recap a second time and
		// the singleton recap session re-fires enter_claude), just
		// re-emit claude_entered as a heads-up to the client. Mismatched
		// renderer is still an error — switching renderers requires
		// Exit Claude first.
		if m.GetRenderer(msg.SessionID) == renderer {
			_ = write(OutMsg{Type: "claude_entered", SessionID: msg.SessionID, Renderer: string(renderer)})
			return
		}
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "renderer_mismatch", Message: "session is already in claude mode with a different renderer"})
		return
	}
	sh, err := m.Get(msg.SessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
		return
	}
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return
	}
	if sh.CurrentCommand() != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "session_busy", Message: "let the current command finish first"})
		return
	}

	// Ensure the per-session Claude conversation UUID exists. Both
	// renderers use --resume <uuid> so the dialogue persists across
	// renderer choices, Exit/re-enter, and Pod restart.
	if _, err := m.EnsureClaudeConvoID(msg.SessionID); err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return
	}

	switch renderer {
	case store.RendererTUI:
		// V0 path: send-keys `claude` into the tmux pane and let the
		// TUI take over the bytes that flow through pty_data.
		if err := sh.EnterClaude(); err != nil {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "enter_failed", Message: err.Error()})
			return
		}
	case store.RendererUI:
		// V1 path: do NOT touch the tmux pane. The pane stays at bash
		// prompt; we'll fork `claude -p ...` on demand from
		// handleClaudePrompt. The frontend will mount ClaudeChatView
		// and start sending claude_prompt frames.
		//
		// Make sure ~/.claude/settings.json points PreToolUse at our
		// bridge script so tool calls trigger the approval flow.
		// Idempotent — re-entering UI mode is cheap.
		if home, herr := os.UserHomeDir(); herr == nil {
			if err := claude.EnsureSettingsHook(home); err != nil {
				slog.Warn("EnsureSettingsHook failed", "session", msg.SessionID, "err", err)
				// Non-fatal: claude will still run, but tool use will
				// be auto-approved instead of asking the user. Surface
				// a soft warning so the UI can show it.
				_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "settings_warning", Message: "could not configure PreToolUse hook: " + err.Error()})
			}
		}
	}

	if err := m.SetMode(msg.SessionID, store.ModeClaude); err != nil {
		slog.Warn("SetMode(claude) failed", "session", msg.SessionID, "err", err)
	}
	if err := m.SetRenderer(msg.SessionID, renderer); err != nil {
		slog.Warn("SetRenderer failed", "session", msg.SessionID, "err", err)
	}
	// Bypass flag default: true (matches the dialog's default checkbox).
	// Absent ↔ legacy clients that don't send the field — keep the
	// safer default so things just work.
	bypass := true
	if msg.BypassPermissions != nil {
		bypass = *msg.BypassPermissions
	}
	if err := m.SetClaudeBypass(msg.SessionID, bypass); err != nil {
		slog.Warn("SetClaudeBypass failed", "session", msg.SessionID, "err", err)
	}
	if err := m.SetTemplateID(msg.SessionID, msg.TemplateID); err != nil {
		slog.Warn("SetTemplateID failed", "session", msg.SessionID, "err", err)
	}
	_ = write(OutMsg{Type: "claude_entered", SessionID: msg.SessionID, Renderer: string(renderer)})
}

func handleExitClaude(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if !requireSessionID(msg, "exit_claude", write) {
		return
	}
	if m.GetMode(msg.SessionID) != store.ModeClaude {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "not_in_claude", Message: "session is not in claude mode"})
		return
	}
	sh, err := m.Get(msg.SessionID)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
		return
	}
	// Dispatch the actual teardown by renderer.
	switch m.GetRenderer(msg.SessionID) {
	case store.RendererTUI, "":
		// V0 path: nudge claude in the pane to exit (it owns the
		// PTY). Empty renderer means a legacy V0 session — same
		// behavior.
		if err := sh.ExitClaude(); err != nil {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "exit_failed", Message: err.Error()})
			return
		}
	case store.RendererUI:
		// V1 path: there is no long-lived claude in the pane. If a
		// claude -p prompt is in flight, the WS handler's claude
		// runner is the one holding the process; ExitClaude here is
		// a no-op as far as the pane is concerned. The caller in
		// ws.go also SIGINTs the in-flight runner via runStates.take.
	}
	if err := m.SetMode(msg.SessionID, store.ModeShell); err != nil {
		slog.Warn("SetMode(shell) failed", "session", msg.SessionID, "err", err)
	}
	if err := m.SetRenderer(msg.SessionID, ""); err != nil {
		slog.Warn("clear renderer failed", "session", msg.SessionID, "err", err)
	}
	_ = write(OutMsg{Type: "claude_exited", SessionID: msg.SessionID})
}

func handleStdin(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if !requireSessionID(msg, "stdin", write) {
		return
	}
	if m.GetMode(msg.SessionID) != store.ModeClaude {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "mode_mismatch", Message: "stdin is only valid in claude mode"})
		return
	}
	sh, err := m.Get(msg.SessionID)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
		return
	}
	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "stdin data must be base64"})
		return
	}
	if err := sh.SendStdin(data); err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "stdin_failed", Message: err.Error()})
		return
	}
}

// handleClaudePrompt forks `claude -p ...` for one user prompt and
// streams the parsed events back via claude_event frames. Only valid
// when the session is in claude mode with renderer=ui. Refuses if a
// prompt is already in flight (one at a time per session).
func handleClaudePrompt(msg InMsg, m *session.Manager, runner *claude.Runner, out chan<- claudeEventEnvelope, runStates *claudeRunStateMap, write func(OutMsg) error) {
	if !requireSessionID(msg, "claude_prompt", write) {
		return
	}
	// If RenderTemplate is set and Text is empty, render the named
	// template server-side and use it as the prompt. This is how the
	// RecapSidebar "Generate" button fires the recap-daily prompt
	// without owning placeholder resolution.
	if strings.TrimSpace(msg.Text) == "" && msg.RenderTemplate != "" {
		dataDir := m.DataDir()
		today := time.Now().Local().Format("2006-01-02")
		// Cwd for the template body — recap sessions resolve to the
		// recap dir below, so make it reflect that. Use the
		// symlink-resolved form (CLI does the same), so paths inside
		// the prompt match what `pwd` will report.
		tplCwd := claudeInvocationCWD()
		if isRecapKind(m, msg.SessionID) {
			rd := recap.Dir(dataDir)
			if resolved, err := filepath.EvalSymlinks(rd); err == nil {
				tplCwd = resolved
			} else {
				tplCwd = rd
			}
		}
		msg.Text = template.Render(msg.RenderTemplate, template.RenderArgs{
			SessionID:   msg.SessionID,
			SummaryPath: summary.Path(dataDir, msg.SessionID),
			Date:        today,
			Cwd:         tplCwd,
			RecapPath:   recap.Path(dataDir, today),
		})
		// The template body itself becomes the user prompt; no template
		// injection on top of it.
		_ = m.SetTemplateID(msg.SessionID, "")
	}
	if strings.TrimSpace(msg.Text) == "" {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "bad_request", Message: "prompt text required"})
		return
	}
	if m.GetMode(msg.SessionID) != store.ModeClaude {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "mode_mismatch", Message: "claude_prompt is only valid in claude mode"})
		return
	}
	if m.GetRenderer(msg.SessionID) != store.RendererUI {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "renderer_mismatch", Message: "claude_prompt requires renderer=ui"})
		return
	}
	if _, busy := runStates.get(msg.SessionID); busy {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "busy", Message: "another prompt is still in flight"})
		return
	}
	convoID, err := m.EnsureClaudeConvoID(msg.SessionID)
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return
	}
	// claude --session-id keys its transcript on (cwd, uuid); see
	// claudeInvocationCWD for the resolution + fallback policy.
	// Recap sessions are pinned to <DATA_DIR>/recaps so Claude has
	// `pwd`-readable access to past recaps as context, and its
	// claude-mem / file-history scopes by this dir alone instead of
	// the user's $HOME.
	cwd := claudeInvocationCWD()
	if isRecapKind(m, msg.SessionID) {
		recapDir := recap.Dir(m.DataDir())
		if err := os.MkdirAll(recapDir, 0o755); err != nil {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "recap_dir_failed", Message: err.Error()})
			return
		}
		// Resolve symlinks (macOS /tmp → /private/tmp) so the cwd
		// matches what the Claude CLI sees when it computes the
		// transcript directory hash. Without this, runner's
		// transcriptExists picks the wrong path and we get
		// "Session ID already in use" errors on every prompt past
		// the first.
		if resolved, err := filepath.EvalSymlinks(recapDir); err == nil {
			cwd = resolved
		} else {
			cwd = recapDir
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	finalText := composePromptText(
		msg.Text,
		m.GetTemplateID(msg.SessionID),
		msg.SessionID,
		summary.Path(m.DataDir(), msg.SessionID),
	)
	pr, err := runner.Prompt(ctx, claude.PromptOptions{
		SessionUUID:       convoID,
		CWD:               cwd,
		Prompt:            finalText,
		BypassPermissions: m.GetClaudeBypass(msg.SessionID),
	})
	if err != nil {
		cancel()
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "claude_spawn_failed", Message: err.Error()})
		return
	}
	runStates.set(msg.SessionID, &claudeRunState{cancel: cancel, stop: pr.Stop})

	stopCh := make(chan struct{})
	// Forward parsed events; release the runStates slot as soon as
	// we see a `result` event so the user can send the next prompt
	// without racing the reaper's pr.Wait().
	go func() {
		defer close(stopCh)
		for ev := range pr.Events {
			payload := claudeEventPayload(ev)
			select {
			case out <- claudeEventEnvelope{sessionID: msg.SessionID, kind: ev.Kind, payload: payload}:
			case <-ctx.Done():
				return
			}
			if ev.Kind == claude.KindResult {
				// take() is idempotent with the reaper's take below.
				_, _ = runStates.take(msg.SessionID)
			}
		}
	}()
	// Reap the process exit + clean up. take() returns false if the
	// slot was already cleared (by exit_claude, or by the result
	// event above) — in those cases the frontend has what it needs
	// and no backstop is emitted. claude_run_ended only fires when
	// the process died without ever emitting `result` AND nobody
	// explicitly exited — i.e., a crash.
	go func() {
		<-stopCh
		waitErr := pr.Wait()
		_, owned := runStates.take(msg.SessionID)
		cancel()
		if owned {
			endMsg := ""
			if waitErr != nil {
				endMsg = waitErr.Error()
			}
			_ = write(OutMsg{
				Type:      "claude_run_ended",
				SessionID: msg.SessionID,
				Message:   endMsg,
			})
		}
	}()
}

// handleToolDecision unblocks a PreToolUse hook waiting in the
// bridge. The toolUseID identifies which pending request to resolve.
func handleToolDecision(msg InMsg, bridge *claude.Bridge, write func(OutMsg) error) {
	if bridge == nil {
		_ = write(OutMsg{Type: "error", Code: "unavailable", Message: "claude bridge not configured"})
		return
	}
	if msg.ToolUseID == "" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "tool_decision requires toolUseId"})
		return
	}
	if msg.Decision != "allow" && msg.Decision != "deny" {
		_ = write(OutMsg{Type: "error", Code: "bad_request", Message: "decision must be 'allow' or 'deny'"})
		return
	}
	// Pending request not found (timed out, already resolved): benign
	// race — nothing to do, don't surface an error to the client.
	_ = bridge.Resolve(msg.ToolUseID, claude.Decision{
		Permission: msg.Decision,
		Reason:     msg.Reason,
	})
}

// isRecapKind reports whether the session is a recap session. Cheap
// lock-protected lookup; safe to call from the prompt hot path.
func isRecapKind(m *session.Manager, sessionID string) bool {
	meta, ok := m.FindByID(sessionID)
	return ok && meta.Kind == store.KindRecap
}

// claudeInvocationCWD returns the directory we run `claude -p` from.
// Preference order:
//  1. /home/alfred — the canonical pod path. Always exists in
//     production; the on-disk transcript cwd-hash uses this path.
//  2. $HOME — for local dev on macOS where /home/alfred doesn't
//     exist. The first prompt creates a new transcript under this
//     cwd, and the same UUID will keep resolving to it.
//  3. "/" — last resort; should never happen on a sane system.
//
// We only check existence, not writability. If the chosen dir
// turns out to be read-only, claude itself will surface that.
func claudeInvocationCWD() string {
	const podHome = "/home/alfred"
	if st, err := os.Stat(podHome); err == nil && st.IsDir() {
		return podHome
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if st, err := os.Stat(home); err == nil && st.IsDir() {
			return home
		}
	}
	return "/"
}
