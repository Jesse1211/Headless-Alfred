# Claude UI Mode — Design Spec

> Status: draft 2026-06-13. Extends the Claude-mode TUI feature shipped on
> 2026-06-13 (`docs/superpowers/specs/2026-06-13-claude-mode-design.md`).

## 1. Goal

This spec adds a **second renderer** for the Claude mode that already
ships in Alfred. The current renderer is xterm.js running Claude's
native TUI — full screen, ANSI, exactly what `claude` looks like in a
real terminal. The new renderer is a ChatGPT-style React chat with
Markdown, code blocks, and tool-use approval cards.

Both renderers are **two views on the same conversation**. The user
flips between them via a header toggle. Underneath, only the Claude
process model changes — interactive CLI for TUI, programmatic
`claude -p --resume <uuid>` for UI — both pointed at the same
conversation UUID on disk. The user experience is "talking to the
same Claude, just drawn differently".

Tool use (`Bash`, `Edit`, `Write`, etc.) is **intercepted by Alfred**
in UI mode: Claude asks, the React UI shows an "Approve / Deny" card,
Alfred returns the decision to Claude.

This is **not** a replacement for the TUI. We keep both renderers.

### 1.0.1 What this is NOT

It's worth being explicit about what's out of scope, because it shapes
several decisions below:

- **Not a shell.** Claude mode = talking to Claude. There is no
  `cd`-ing inside Claude mode. If the user wants to navigate the
  filesystem, they exit Claude mode (Exit button) and use the shell
  view. Inside Claude mode, the only thing the user does is type
  prompts; the only thing Claude does is respond and (with the user's
  approval) call tools.
- **Not a multi-conversation hub.** One Claude conversation per Alfred
  session. To start a parallel chat, create a parallel Alfred session.
- **Not a permission tuning surface.** v1 asks before every tool use.
  No fine-grained allow-lists, no "auto-allow read tools" knob. The
  user clicks Allow/Deny each time. (We can add "Allow always for
  Bash" later via Claude's own `~/.claude/settings.json`, but it's
  v1.5.)

### 1.1 Non-goals (v1)

- Replacing the TUI. Both paths coexist; we can remove the TUI in a
  future release if the UI eats its lunch.
- Multiple Claude conversations per Alfred session. One Claude
  conversation per Alfred session, switchable between views.
- Mobile UI. Same desktop assumption as the rest of the workbench.
- Cost tracking beyond per-turn token display. No budget caps, no
  per-month dashboards in v1.

---

## 2. User flow

```
1. Alfred session in shell mode (the current default). User does
   regular shell things, eventually navigates somewhere they want
   Claude's attention.
2. User clicks the Claude button (or types `claude` / `/claude`) →
   enters Claude mode. The current behavior is "TUI renderer": the
   xterm.js view shows Claude's native screen.
3. NEW: header now shows three buttons: [ TUI ✓ ] [ UI ] [ Exit Claude ].
   The first two are the renderer toggle; both are inside Claude mode.
4. User clicks "UI" → renderer changes to the React chat view.
   - The TUI's interactive `claude` process is killed.
   - The conversation UUID (assigned the first time Claude was entered
     in this Alfred session, stored in sessions.json) is reused.
   - No `claude` process is running between user prompts in UI mode —
     each prompt forks a fresh `claude -p --resume <uuid>` (§3).
5. User types a prompt in the chat input → backend forks ONE
   `claude -p` process → streams stream-json events back via WS frames
   (`claude_event`) → React renders Markdown / code / etc.
6. Claude wants to run `bash: ls /` → PreToolUse hook fires → blocks
   on localhost endpoint → Alfred pushes `tool_approval_request` to
   React → React shows "Claude wants to run bash: ls /. [Allow] [Deny]"
   → user clicks → backend returns the decision to the hook → hook
   returns to Claude → Claude proceeds or skips.
7. User clicks "TUI" → backend respawns interactive `claude --resume
   <uuid>` in the tmux pane → frontend switches back to xterm.js. The
   same conversation is now visible in TUI (because --resume reads the
   same transcript file).
8. User clicks "Exit Claude" → both renderer paths tear down, tmux
   pane goes back to bash (existing flow). The conversation UUID
   stays on disk; entering Claude again resumes it.
```

The renderer toggle is the only new control. Everything else is wiring.

### 2.1 cwd

The conversation is bound to one cwd at creation time (Claude CLI
requires it for `--resume`). cwd is "whatever the user's bash was in
when they first entered Claude mode for this Alfred session". After
that, cwd doesn't drift inside Claude mode, because **Claude mode is
not a shell** — there is no `cd`. Switching between TUI and UI
renderers does not change cwd: both renderers invoke claude from the
same tmux pane that's still sitting at the same prompt.

This rules out an entire class of edge cases the previous draft
worried about.

### 2.2 Switching renderer mid-turn

If the user clicks "UI" while the TUI is mid-conversation (Claude is
streaming a response, or waiting on a tool), or vice versa, the
toggle is **disabled** until the current turn finishes:

- UI mode: backend tracks `inFlightPrompt` per session. While true,
  both the "TUI" button and the prompt input show as busy.
- TUI mode: harder — we can't peek into Claude's TUI state. Best
  effort: the "UI" button is enabled but clicking it shows a confirm
  dialog "Claude may be in the middle of work. Switch anyway?" with
  a clear "Switching kills the current Claude process and starts a
  fresh one on the next prompt". For v1 we accept this lossy switch
  because we can't synchronously detect TUI activity.

---

## 3. Architecture: process model

### 3.0 User-visible equivalence to "claude in a terminal"

The user's mental model is "I'm chatting with one Claude, the same
way I would in a terminal — I send a prompt, it replies, I send the
next prompt, it remembers what we just talked about." Every
implementation decision below must preserve that property: same
conversation across the renderer toggle, same conversation across
prompts, same conversation across Pod restarts (until the user
chooses Exit Claude).

Concretely, that's why we lean on `--resume <uuid>` rather than
trying to keep a process alive: the Claude CLI's own session model
already guarantees "next prompt continues the conversation", and
we just route prompts to it.

### 3.1 At most one Claude process at a time

For a given Alfred session, at most ONE Claude process exists:

| User intent | Process running in tmux pane |
|---|---|
| Just entered Claude (default TUI) | `claude --resume <uuid>` (or `claude` first time → claude assigns uuid) |
| In UI mode, idle (no prompt sent yet) | nothing (pane has bash prompt) |
| In UI mode, prompt in flight | `claude -p --output-format stream-json --include-partial-messages --resume <uuid>` (one per prompt) |
| In UI mode, idle (turn finished) | nothing again |

UI mode is **one-shot per prompt** because `-p` is one-shot (confirmed
via Claude Code docs §1). After a prompt finishes, the `claude -p`
process exits and the pane is back at bash prompt. This is fine — we
never let the user know there isn't a long-running process.

### 3.2 Conversation UUID

When the user enters Claude mode for the first time, the backend
generates a v4 UUID and stores it as `claudeSessionID` on the Alfred
session's metadata (in `sessions.json`). Every subsequent `claude`
invocation for that Alfred session uses `--session-id <uuid>` (first
time) or `--resume <uuid>` (every time after).

The UUID is **per Alfred session** (one Claude conversation per pane).
If you want a second conversation, create a second Alfred session.

The conversation transcript lives at:
```
~/.claude/projects/<cwd-encoded>/<uuid>.jsonl
```
on the PVC, persisted across Pod restarts. Pod restart loses the live
Claude process but keeps the transcript — next prompt picks up where
you left off.

### 3.3 Why the alfred session stays in tmux

We're tempted to skip tmux for UI mode (just fork claude -p directly
from alfred-server). But keeping tmux:

- Lets the user toggle to TUI without re-architecting.
- Reuses the existing multi-session lifecycle machinery (session limit,
  Reconcile, session_closed, etc.).
- Keeps cwd in the bash that runs claude consistent with what TUI sees.

The trade-off: every UI-mode prompt spawns a `claude -p` via
`tmux send-keys`, then the alfred parser has to capture stdout. We can
do this with a **redirect** in the send-keys: instead of
```
claude -p ... <prompt>
```
we send
```
claude -p ... <prompt> > /data/sessions/<sid>/claude.stream 2>&1
```
and a goroutine tails `claude.stream` line by line, parses each JSON
event, wraps in a `claude_event` WS frame, pushes to React. (Or we
can use a named pipe / FIFO instead of a regular file — either is
fine. Regular file is simpler.)

The same `claude.stream` file is truncated at the start of each new
prompt (or rotated by sequence number; TBD in §4 implementation
detail).

---

## 4. WS protocol additions

Mode terminology in this codebase (post-change): a session has a
**mode** (`shell` | `claude`) and, when in `claude`, a **renderer**
(`tui` | `ui`). The existing `claude_entered` / `claude_exited` events
keep their meaning ("entered/left claude mode"); they carry the
initial renderer in a new field.

New `InMsg` (client → server):

| Type | Fields | Semantics |
|---|---|---|
| `set_renderer` | `sessionID`, `renderer: "tui" \| "ui"` | Switch between TUI and UI renderers without leaving Claude mode. Refused if `inFlightPrompt`; for TUI→UI, shows the user a "may interrupt Claude" confirm on the frontend before sending. |
| `claude_prompt` | `sessionID`, `text` | UI-renderer only. Send one prompt; backend forks `claude -p --resume <uuid>` with this text. |
| `tool_decision` | `sessionID`, `toolUseID`, `decision: "allow" \| "deny"` | Resolve a pending tool-use approval request raised by the PreToolUse hook. |
| `interrupt` | `sessionID` | UI-renderer only. SIGINT the in-flight claude -p (user clicked "Stop"). |

New `OutMsg` (server → client):

| Type | Fields | Semantics |
|---|---|---|
| `renderer_changed` | `sessionID`, `renderer` | Backend completed a renderer switch. Frontend mounts the matching view. |
| `claude_event` | `sessionID`, `kind`, `payload` | UI-renderer only. One parsed stream-json event. `kind` is one of `system`, `text_delta`, `text_block_end`, `tool_use_start`, `tool_use_end`, `message_delta` (usage), `message_stop`, `result`. `payload` carries the relevant subset of the source JSON. |
| `tool_approval_request` | `sessionID`, `toolUseID`, `tool`, `input`, `display: { title, summary, danger }` | Claude wants to run a tool; frontend shows the Approve/Deny card. The PreToolUse hook is BLOCKED in the pod until `tool_decision` arrives. |
| `claude_error` | `sessionID`, `code`, `message` | Spawn failed, parse failed, claude exited non-zero with no result event, etc. |

Updated existing frames:

- `claude_entered`: now also carries `renderer` (defaults to `"tui"`,
  the legacy behavior). Frontend uses this on initial connect to pick
  the view.
- `idle` / `reattach`: gain a `renderer` field next to `mode`. Empty
  string in legacy clients/sessions → assume `"tui"`.

We deliberately do NOT add `enter_claude_ui`-style events; entering
Claude is one event (`claude_entered`), and renderer is just a sub-
state of that.

---

## 5. Tool intercept design — the bridge

This is the critical mechanism. Claude has no async `wait-for-user`
ability built in; PreToolUse hooks **must return a decision synchronously**.
Our trick: the hook is a **blocking HTTP call** to alfred-server.

### 5.1 Files written to PVC

When a session enters UI mode for the first time, the backend writes:

`~/.claude/settings.json` (per-user, not per-session):
```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": ".*",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/alfred-claude-bridge"
          }
        ]
      }
    ]
  }
}
```

A small shell script at `/usr/local/bin/alfred-claude-bridge` (shipped in
the Dockerfile) reads the tool-call info from stdin (Claude passes JSON
on stdin to the hook), then issues a **blocking HTTP POST** to alfred-
server, e.g.
```
POST http://127.0.0.1:8080/internal/claude-tool-decision
```
with the tool details. The HTTP handler:

1. Looks up the Alfred session whose Claude conversation this is.
2. Pushes a `tool_approval_request` WS frame to any connected client(s).
3. Blocks (Go: channel receive) until either:
   - A `tool_decision` frame comes in from the user.
   - 5 minutes elapse (timeout → deny).
   - The WS connection closes (timeout → deny).
4. Returns JSON `{ "permissionDecision": "allow" | "deny" }` to the hook
   script (via the HTTP response body).
5. The hook script prints that JSON to its own stdout, which Claude reads
   to decide whether to proceed.

This endpoint is **on a separate Go listener bound to 127.0.0.1 only**,
NOT the public alfred-server. Reasons:
- It bypasses normal auth (we trust anything connecting from inside the
  pod).
- It uses long-polling (5 min); we don't want it tangling with the
  request handlers of the public server.
- The port is fixed (e.g. 8090) and only listens on lo.

The endpoint itself doesn't need a sessionID in the request body —
Claude includes `session_id` in the hook payload, which Alfred uses
to look up the right Alfred session via the `claudeSessionID` ↔
`alfredSessionID` map.

### 5.2 v1: every tool use asks

There is **no "Allow always"** in v1. Every Bash / Edit / Write / Read /
Glob / Grep / etc. call surfaces a card. Users dismiss them by
clicking Allow or Deny. This is intentionally noisy — see §1.0.1.

v1.5 will add an "Allow this and similar for the rest of this turn"
checkbox or a per-session policy panel (TBD), and will use Claude's
own `~/.claude/settings.json` `permissions.allow` array to persist.
Out of scope here.

### 5.3 Pre-blacklist (out of scope)

Letting the user pre-blacklist tools (e.g., "never let Claude touch
the filesystem in this session") is also v1.5. v1 keeps the surface
minimal: just the per-call Allow/Deny.

---

## 6. Frontend changes

### 6.1 New `web/src/features/claude-ui/` package

- `ClaudeChatView.tsx`: the main view; renders the chat history,
  composer, tool-approval cards, usage footer.
- `ClaudeMessageRenderer.tsx`: takes the accumulated event list for a
  turn and renders Markdown (assistant text), code blocks with syntax
  highlight (we already have a renderer for this in ChatStream — share
  it), thinking blocks (collapsed by default), tool_use cards.
- `ToolApprovalCard.tsx`: the per-tool-call "Allow / Deny" card
  (v1 only — no "always" button, per §5.2).
- `useClaudeStream.ts`: hook that accumulates `claude_event` frames
  into a per-turn message structure, exposes the current turn's state
  (text streaming in, tool requested, finished, etc.).

### 6.2 Header buttons

While in Claude mode:
```
[ TUI ]  [ UI ]  [ Exit Claude ]
```

Highlight the active one. Both inner buttons disabled while
`inFlight==true`.

### 6.3 Reuse existing components

- Markdown rendering: `marked` or `react-markdown` (add dep).
- Code block syntax highlight: `prismjs` (add dep) or use the existing
  `ansi-to-html` if we don't want a heavy SVG.
- The Anthropic credentials dialog (already shipped) becomes the
  prerequisite — no Claude UI without creds.

### 6.4 Composer behavior

In UI mode the composer is a multi-line textarea (Shift+Enter newline,
Enter sends). While `inFlight`, the composer is disabled and a "Stop"
button appears (sends `interrupt`).

---

## 7. Backend changes

### 7.1 New package `internal/claude/`

- `runner.go`: `Runner.Prompt(sessionID, convoID, cwd, prompt) (stream <-chan Event, done <-chan struct{}, err error)` — spawns
  `claude -p --resume <id> --output-format stream-json --include-partial-messages` inside the session's
  tmux pane via send-keys (or direct exec; pick one in implementation),
  tails its stdout, parses line-by-line, wraps each line into an
  `Event` struct, delivers on channel.
- `event.go`: typed event structs — `Event`, `TextDelta`, `ToolUse`,
  `MessageDelta`, `Result`, etc. — the union the runner emits.
- `bridge.go`: the localhost HTTP listener for the PreToolUse hook
  (§5.1). Per-process map of `toolUseID → resolveCh`. Endpoint
  blocks on a Go channel until `Manager.ResolveTool(id, decision)`.

### 7.2 `internal/session/manager.go`

Add `ClaudeSessionID string` to `SessionMeta` (per Alfred session, the
Claude conversation UUID). New methods:
- `EnsureClaudeConvoID(sessionID)` → returns the UUID, generates+persists if absent.
- `ResolveTool(toolUseID, decision)`: called from the WS handler when
  `tool_decision` arrives.

### 7.3 `internal/api/ws.go`

- Handle new `InMsg` types (§4).
- `set_renderer { renderer: "ui" }`: kill any live TUI `claude`
  process in the pane (via existing `ExitClaude`-like cleanup but
  WITHOUT respawning bash — we'll just leave the pane at the prompt
  for the next claude -p to use). Update session state
  `renderer = "ui"`. Send `renderer_changed` back.
- `set_renderer { renderer: "tui" }`: if a `claude -p` is in flight,
  refuse with an error (frontend already disables the button, this
  is a backstop). Otherwise, send-keys `claude --resume <uuid>` into
  the pane and update `renderer = "tui"`. Send `renderer_changed`.
- `claude_prompt`: only valid when `renderer == "ui"`. Launch via
  `claude.Runner.Prompt(...)`, forward each parsed event as a
  `claude_event` frame.
- `tool_decision`: call `Manager.ResolveTool(toolUseID, decision)`.
- `interrupt`: SIGINT the in-flight `claude -p` PID. The Runner's
  reader goroutine will see EOF, finish flushing, and we send a
  `claude_event { kind: "interrupted" }` followed by inflight=false.

### 7.4 Dockerfile

Ship `/usr/local/bin/alfred-claude-bridge` (the small shell script
that's the PreToolUse hook). Also ensure `~/.claude/settings.json`
is seeded (if not present) at first UI-mode entry, NOT in the Docker
image — settings.json is a user file and should be created lazily
per pod, in case the user wants to edit it.

### 7.5 No database changes

`sessions.json` gains the `claudeSessionID` field (omitempty).
Conversation transcripts live in `~/.claude/projects/.../<uuid>.jsonl`
managed by Claude. We don't touch them.

---

## 8. Edge cases

1. **Claude binary not authenticated**: `claude -p` exits with an error
   message about login. We surface this as `claude_error` with code
   `not_authenticated` and the React UI shows "Run `claude` in a session
   and complete /login, or upload credentials via the gear menu".

2. **Hook timeout (user closed tab)**: PreToolUse hook blocks for 5
   minutes, then returns `deny`. Claude sees a denied tool use, writes
   a "user denied" message, finishes. The user comes back to a finished
   chat with a polite refusal.

3. **WS reconnect mid-prompt**: backend buffers `claude_event` frames
   in a per-session ring buffer (last 256 KiB or 1000 events). On
   reconnect, we replay the buffer for the active turn so the React UI
   renders the in-progress text correctly. (Same idea as the multi-
   session reattach mechanism, scoped to one turn.)

4. **Two browser tabs, one Alfred session, both in UI mode**: both
   receive `tool_approval_request`. The first to send a `tool_decision`
   wins; the second sees a "stale" message acknowledging.

5. **User clicks "Stop" mid-tool-execution**: backend sends SIGINT
   to the claude -p process. Claude CLI's behavior on SIGINT in
   stream-json mode is undocumented; assume it exits with a non-zero
   code. We treat it as if the turn ended with an error. The hook may
   or may not return before SIGINT propagates — we don't care, the
   user wanted out.

6. **(Removed — cwd doesn't drift because the user can't `cd`
   inside Claude mode; see §1.0.1 and §2.1.)**

7. **Claude wants to run a tool we don't have a UI affordance for**
   (e.g. a fancy MCP server tool): the approval card just shows the
   tool name + raw `input` JSON. User decides based on that. We don't
   need rich UI for every imaginable tool in v1.

8. **Hook script can't reach the localhost bridge** (alfred-server
   crashed mid-conversation): hook returns deny by default; Claude
   sees deny and gracefully refuses.

9. **User edits `~/.claude/settings.json` manually** to add/remove
   hooks: we don't conflict — we own ONLY the `hooks.PreToolUse`
   array, written atomically. We don't touch other keys.

10. **Sessions.json migration**: old sessions without `claudeSessionID`
    field. Loader treats absent as `""`. EnsureClaudeConvoID generates
    fresh on first UI-mode entry. No migration needed.

---

## 9. Phasing

1. **Phase 1**: backend `internal/claude/runner.go` — spawn
   `claude -p --resume <uuid> --output-format stream-json
   --include-partial-messages`, parse stdout line-by-line into typed
   events, ship via a Go channel. Unit-tested with a fake
   stream-json fixture. No tmux, no UI yet.
2. **Phase 2**: `internal/claude/bridge.go` — the localhost HTTP
   listener that backs the PreToolUse hook. Plus the shell-script
   hook itself shipped in the Dockerfile. Manual `curl` smoke
   verifies "Claude calls a tool → hook hangs on the bridge →
   /resolve endpoint sets the decision → hook returns → Claude
   proceeds".
3. **Phase 3**: WS protocol — add `set_renderer`, `claude_prompt`,
   `tool_decision`, `interrupt` inbound; `renderer_changed`,
   `claude_event`, `tool_approval_request`, `claude_error` outbound.
   `Manager` learns the per-session `Renderer` and the
   `ClaudeSessionID` (UUID). `idle`/`reattach`/`claude_entered`
   gain the renderer field.
4. **Phase 4**: React `ClaudeChatView` skeleton — renders incoming
   `claude_event` frames as raw blocks (no Markdown yet, just text).
   Composer textarea + Stop button. End-to-end: type prompt, see
   raw text stream back. Tool calls show as unstyled "Allow / Deny"
   buttons.
5. **Phase 5**: rich rendering — react-markdown + remark-gfm for
   assistant text; syntax-highlighted code blocks; collapsed
   thinking blocks. Tool-call cards get proper styling.
6. **Phase 6**: header three-way toggle "TUI ✓ / UI / Exit Claude".
   Backend `set_renderer` flips between interactive `claude` in
   the tmux pane and the on-demand `claude -p`. Confirm dialog on
   TUI→UI mid-turn.
7. **Phase 7**: edge-case hardening + token/cost footer (from
   `message_delta.usage`, with client-side dollar estimate using
   published pricing) + Pod-restart replay + CONTEXT.md + README
   docs.

Estimate: 15–22 hours focused work, 3–4 calendar days.

---

## 10. Acceptance

Done when:
- User clicks "UI" button in claude mode → sees Markdown-rendered
  chat. Types a question → tokens stream in.
- Claude asks to run `bash: ls` → card appears → user clicks Allow →
  result appears under the card → Claude continues.
- Every tool use yields a card; user clicks Allow → tool runs and
  result appears below the card; Deny → tool is skipped and Claude
  sees a "user denied" message.
- Click "TUI" → back to xterm.js → conversation continuation visible
  in TUI (proves `--resume <uuid>` works across modes).
- Pod restart (`kubectl rollout`) preserves transcript: re-enter UI,
  see full history.
- Existing `/claude` TUI flow unaffected — all existing 17 E2E pass.
