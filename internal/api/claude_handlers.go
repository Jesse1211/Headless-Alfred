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
	"github.com/jesseliu/headless-alfred/internal/claudestate"
	"github.com/jesseliu/headless-alfred/internal/recap"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
	"github.com/jesseliu/headless-alfred/internal/summary"
	"github.com/jesseliu/headless-alfred/internal/template"
)

// composePromptText assembles the final prompt text that will be
// piped to `claude -p` stdin. Each non-empty rendered template is
// appended after the user's message separated by a markdown
// horizontal rule so Claude can tell what came from the user and
// what came from the harness.
//
// templateIDs is the list of templates to inject for THIS prompt.
// The frontend sends it per-prompt (per-prompt selectable checkboxes
// in the composer); when nil/empty the call site can choose to fall
// back to the session-default TemplateID for backwards-compat with
// older clients. Order of templateIDs is preserved — the rendered
// blocks concatenate in the same order.
//
// Unknown IDs and templates that render to "" are silently skipped
// (template.Render returns "" for unknown IDs). That's the right
// behaviour for forward-compat: if the client offers a template
// that the server doesn't know yet (or vice versa), we just drop
// it rather than 400.
func composePromptText(userText string, templateIDs []string, sessionID, summaryPath string) string {
	out := userText
	for _, id := range templateIDs {
		rendered := template.Render(id, template.RenderArgs{
			SessionID:   sessionID,
			SummaryPath: summaryPath,
		})
		if rendered == "" {
			continue
		}
		out += "\n\n---\n" + rendered
	}
	return out
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

// claudeRunStateEntry pairs a sessionID with its run state — used by
// takeAll so disconnect cleanup can both kill the runner AND tell
// server state about it (Apply EventClaudeRunEnded). Without the
// session id we'd kill the process but leave InFlight=true forever.
type claudeRunStateEntry struct {
	sessionID string
	state     *claudeRunState
}

// takeAll removes and returns every state along with its sessionID.
// Used on WS disconnect to stop in-flight runners AND signal Apply
// so the trailing turn doesn't get stuck at Done=false.
func (s *claudeRunStateMap) takeAll() []claudeRunStateEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]claudeRunStateEntry, 0, len(s.m))
	for sid, st := range s.m {
		out = append(out, claudeRunStateEntry{sessionID: sid, state: st})
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
	case claude.KindThinkingDelta:
		return ev.ThinkingDelta
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
	case claude.KindTaskStarted:
		return ev.TaskStarted
	case claude.KindTaskNotification:
		return ev.TaskNotification
	case claude.KindTaskUpdated:
		return ev.TaskUpdated
	case claude.KindHookStarted:
		return ev.HookStarted
	case claude.KindHookResponse:
		return ev.HookResponse
	case claude.KindUnknown:
		return ev.Unknown
	}
	return nil
}

func handleEnterClaude(msg InMsg, m *session.Manager, write func(OutMsg) error) {
	if !requireSessionID(msg, "enter_claude", write) {
		return
	}
	renderer, sh, ok := validateEnterClaude(msg, m, write)
	if !ok {
		return
	}
	if !enterClaudeRenderer(msg, renderer, sh, write) {
		return
	}
	persistEnterClaude(msg, m, renderer)
	_ = write(OutMsg{Type: "claude_entered", SessionID: msg.SessionID, Renderer: string(renderer)})
}

// validateEnterClaude resolves the renderer and the target shell,
// running every precondition check. On any failure it writes the
// appropriate error frame and returns ok=false. The idempotent
// "already in claude mode, same renderer" case writes claude_entered
// itself and also returns ok=false (nothing left to do).
func validateEnterClaude(msg InMsg, m *session.Manager, write func(OutMsg) error) (store.ClaudeRenderer, session.Shell, bool) {
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
		return "", nil, false
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
			return "", nil, false
		}
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "renderer_mismatch", Message: "session is already in claude mode with a different renderer"})
		return "", nil, false
	}
	sh, err := m.Get(msg.SessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "unknown_session", Message: "no such session"})
		return "", nil, false
	}
	if err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return "", nil, false
	}
	if sh.CurrentCommand() != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "session_busy", Message: "let the current command finish first"})
		return "", nil, false
	}

	// Ensure the per-session Claude conversation UUID exists. Both
	// renderers use --resume <uuid> so the dialogue persists across
	// renderer choices, Exit/re-enter, and Pod restart.
	if _, err := m.EnsureClaudeConvoID(msg.SessionID); err != nil {
		_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "manager_error", Message: err.Error()})
		return "", nil, false
	}
	return renderer, sh, true
}

// enterClaudeRenderer performs the renderer-specific side effect. The
// TUI path can hard-fail (returns false after writing enter_failed);
// the UI path only ever soft-warns, so it always returns true.
func enterClaudeRenderer(msg InMsg, renderer store.ClaudeRenderer, sh session.Shell, write func(OutMsg) error) bool {
	switch renderer {
	case store.RendererTUI:
		// V0 path: send-keys `claude` into the tmux pane and let the
		// TUI take over the bytes that flow through pty_data.
		if err := sh.EnterClaude(); err != nil {
			_ = write(OutMsg{Type: "error", SessionID: msg.SessionID, Code: "enter_failed", Message: err.Error()})
			return false
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
	return true
}

// persistEnterClaude commits the session's claude-mode metadata. Each
// setter only soft-warns on failure (the in-memory transition already
// happened); none abort the enter.
func persistEnterClaude(msg InMsg, m *session.Manager, renderer store.ClaudeRenderer) {
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
func handleClaudePrompt(msg InMsg, m *session.Manager, runner *claude.Runner, out chan<- claudeEventEnvelope, runStates *claudeRunStateMap, csMgr *claudestate.SessionManager, write func(OutMsg) error) {
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
	//
	// For chat sessions, ask tmux for the pane's current working dir
	// (`pane_current_path`). This makes "I cd'd to /foo then opened
	// Claude" actually start Claude in /foo, so `ls`, `Read`, `Write`,
	// `Glob` all behave like the shell session the user just left.
	// Falls back to claudeInvocationCWD() if tmux returns empty (e.g.,
	// pane just died and a respawn hasn't settled).
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
	} else if sh, err := m.Get(msg.SessionID); err == nil {
		if paneCwd := sh.CurrentCWD(); paneCwd != "" {
			// Same symlink-resolution as the recap branch: claude CLI
			// computes the transcript dir hash from the realpath of cwd,
			// so passing /tmp/X when /tmp -> /private/tmp resolves to
			// /private/tmp/X would break transcript lookups on macOS.
			if resolved, err := filepath.EvalSymlinks(paneCwd); err == nil {
				cwd = resolved
			} else {
				cwd = paneCwd
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Per-prompt template selection: if the client sent a Templates
	// array (any non-nil value, including the empty list — that
	// means "explicitly inject nothing"), honor it. Older clients
	// without the field fall back to the session-default TemplateID
	// for backwards compat.
	templateIDs := msg.Templates
	if templateIDs == nil {
		if id := m.GetTemplateID(msg.SessionID); id != "" {
			templateIDs = []string{id}
		}
	}
	finalText := composePromptText(
		msg.Text,
		templateIDs,
		msg.SessionID,
		summary.Path(m.DataDir(), msg.SessionID),
	)
	// Echo the FULL composed prompt back to the client so the user can
	// see exactly what was sent to Claude (including server-injected
	// template bodies). The frontend stashes this on the in-flight
	// turn and UserPromptBubble renders it under a "Show full prompt"
	// toggle — transparency for the token bill.
	_ = write(OutMsg{Type: "user_prompt", SessionID: msg.SessionID, Text: finalText})
	// Register the optimistic turn server-side and announce it back to
	// the originating client so the frontend can reconcile its
	// placeholder turn by clientNonce.
	dispatchClaudePromptBegin(msg.SessionID, msg.ClientNonce, finalText, csMgr, write)
	pr, err := runner.Prompt(ctx, claude.PromptOptions{
		SessionUUID:       convoID,
		CWD:               cwd,
		Prompt:            finalText,
		BypassPermissions: m.GetClaudeBypass(msg.SessionID),
	})
	if err != nil {
		cancel()
		// BeginTurn already registered an optimistic in-flight turn via
		// dispatchClaudePromptBegin above. Without a compensating
		// finalize, that turn stays Done=false forever and the
		// composer never unlocks (only a server restart's
		// finalizeStaleTrailingTurn would recover it). Route a
		// synthesized ClaudeError through Apply so InFlight clears and
		// the turn closes as an error block — same finalize path the
		// runner-died case uses.
		dispatchClaudeError(msg.SessionID, "claude_spawn_failed", err.Error(), csMgr, write)
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
			// Route through Apply so server state's InFlight clears
			// and the trailing turn is marked Done — without this the
			// frame would go out (possibly to a dead conn) but server
			// state would persist InFlight=true forever, and the next
			// reconnect's /claude-state hydrate would still show
			// "Claude is thinking…".
			dispatchClaudeRunEnded(msg.SessionID, endMsg, csMgr, write)
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
