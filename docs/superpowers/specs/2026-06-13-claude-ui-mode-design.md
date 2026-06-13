# Claude UI Mode — Design Spec

> Status: draft 2026-06-13. Extends the Claude-mode TUI feature shipped on
> 2026-06-13 (`docs/superpowers/specs/2026-06-13-claude-mode-design.md`).

## 1. Goal

This spec adds a **second renderer** for the Claude mode that already
ships in Alfred. The current renderer is xterm.js running Claude's
native TUI — full screen, ANSI, exactly what `claude` looks like in a
real terminal. The new renderer is a ChatGPT-style React chat with
Markdown, code blocks, and tool-use approval cards.

The user **picks the renderer once** when entering Claude mode, via
a small "Start Claude" dialog with two radio buttons. Once chosen,
the renderer is **locked** for the lifetime of that Claude session —
to switch, the user has to Exit Claude and re-enter. This is a
deliberate simplification: TUI and UI use very different process
models (interactive `claude` vs one-shot `claude -p --output-format
stream-json`), and trying to swap mid-conversation would require
killing one process and bootstrapping the other in a way that's
brittle and easy to get wrong. Locking the renderer trades a small
amount of flexibility for a much simpler implementation.

What stays consistent across re-entries: the conversation. Each
Alfred session has one Claude conversation UUID (persisted in
`sessions.json`); re-entering Claude after Exit always passes
`--resume <uuid>`, so the dialogue continues.

Tool use (`Bash`, `Edit`, `Write`, etc.) is **intercepted by Alfred**
in UI mode: Claude asks, the React UI shows an "Approve / Deny" card,
Alfred returns the decision to Claude.

This is **not** a replacement for the TUI. We keep both renderers as
parallel choices.

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

### 1.0.2 What this IS (preserved from V0)

Claude mode is **per-session**, exactly the same way shell mode is.
That means:

- The user can have Session A running Claude in UI mode AND Session
  B running shell commands AND Session C running Claude in TUI mode,
  all at the same time. Clicking around the sidebar to switch between
  them does NOT interrupt any running Claude prompt or shell command.
- Closing the browser tab does NOT kill any Claude process. tmux
  panes (TUI) and detached `claude -p` invocations (UI) keep going.
  When the user reconnects, they see what they missed (transcript
  replay from disk for UI; xterm reattach for TUI).
- Each Claude conversation is independent. Session A's UUID is
  different from Session C's UUID. Closing Session A does not affect
  Session C's conversation in any way.

This is the V0 multi-session model applied uniformly to Claude. No
new global state is introduced.

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
2. User clicks the Claude button (or types `claude` / `/claude`).
   Instead of dropping straight into the (legacy) TUI, the frontend
   opens a small "Start Claude" dialog:

   ┌─────────────────────────────────┐
   │ Start Claude in 'My Session'    │
   │                                 │
   │ ○ TUI — Claude's native screen  │
   │ ● UI  — Markdown chat with tool │
   │         approval cards          │
   │                                 │
   │  [Cancel]            [Start]    │
   └─────────────────────────────────┘

3. User picks one and clicks Start.
4a. TUI chosen: backend follows the V0 path exactly — send-keys
    `claude --resume <uuid>` (or `claude` first time, which assigns a
    fresh UUID we capture from the welcome screen — see §3.2), pane
    transitions to xterm.js view. Renderer is locked to "tui" for
    this Claude session.
4b. UI chosen: backend records `renderer = "ui"` on the session, but
    does NOT start a claude process yet. The pane stays at bash
    prompt. Frontend mounts the new ChatClaudeView with an empty
    history. Renderer is locked to "ui".
5. (UI only) User types a prompt in the chat input → backend forks
   ONE `claude -p --resume <uuid> --output-format stream-json` →
   streams events back via `claude_event` frames → React renders
   Markdown / code / etc.
6. (UI only) Claude wants to run `bash: ls /` → PreToolUse hook
   fires → blocks on localhost endpoint → Alfred pushes
   `tool_approval_request` → React shows "Claude wants to run bash:
   ls /. [Allow] [Deny]" → user clicks → backend resolves the hook
   → Claude proceeds or skips.
7. User clicks "Exit Claude" → renderer's claude process is killed
   (TUI path uses the existing V0 ExitClaude flow; UI path SIGINTs
   the latest claude -p if one is in flight, otherwise nothing to
   kill); tmux pane respawns bash; renderer field cleared. The
   conversation UUID stays in sessions.json.
8. User wants to change renderer: simply Exit Claude and re-enter.
   The Start Claude dialog appears again; picking the other option
   resumes the SAME conversation in the new renderer (because --resume
   <uuid> reads the same transcript file on disk).
```

No mid-conversation renderer toggle. Two new controls total: the
"Start Claude" dialog, and (UI-only) the existing chat input plus
its Stop button.

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

### 2.2 No mid-conversation renderer switching

The renderer is locked once chosen. Want to switch? Exit Claude
(which keeps the conversation UUID on disk), pick the other renderer
on re-entry. The dialogue continues — same `--resume <uuid>`,
different process model.

This sidesteps an entire class of bugs around "we have a TUI claude
holding the pane AND we want to start a `-p` process for the same
conversation". Linux process model lets us do it, but the ergonomics
(does the TUI claude write to disk before we kill it?) are not worth
the cost in v1.

### 2.3 Multi-session parallel behavior

Per §1.0.2: each Alfred session has its own mode + renderer + claude
process. Starting Claude in Session A does not impact Sessions B,
C, etc.

For UI-renderer Claude: while a prompt is in flight in Session A,
the user can still click Session B, type shell commands there,
watch Session A's prompt finish (because all sessions broadcast on
the same WS, the frontend just chooses which view to render based
on the selected session). They can return to A at any time and the
chat is up to date.

For TUI-renderer Claude: same as V0. The xterm reattaches when
selected.

---

## 3. Architecture: process model

### 3.0 User-visible equivalence to "claude in a terminal"

The user's mental model is "I'm chatting with one Claude, the same
way I would in a terminal — I send a prompt, it replies, I send the
next prompt, it remembers what we just talked about." Every
implementation decision below must preserve that property: same
conversation across renderer choices (Exit → re-enter with the
other renderer continues the dialogue), across prompts, across Pod
restarts (until the user chooses Exit Claude AND deletes the
session).

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

- Lets the user pick TUI as the renderer without us re-architecting
  the V0 path; the V0 code runs verbatim.
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
(`tui` | `ui`). The renderer is chosen at entry time and locked until
Exit Claude.

Modified existing frames:

- `enter_claude` (inbound, V0): gains a required `renderer: "tui" |
   "ui"` field. Server rejects if missing (no backward-compat —
   frontend always sends after this change ships).
- `claude_entered` (outbound, V0): gains `renderer` echoing back the
  chosen value. Frontend uses this to mount the matching view.
- `idle` / `reattach` (outbound, V0): gain a `renderer` field for
  sessions currently in claude mode. Empty string means "session is
  not in claude mode" (or for legacy data, defaults to `"tui"`).

New `InMsg` (client → server) — all only valid when `mode == "claude"`
AND `renderer == "ui"`:

| Type | Fields | Semantics |
|---|---|---|
| `claude_prompt` | `sessionID`, `text` | Send one prompt. Backend forks `claude -p --resume <uuid>` with this text. Rejected if a previous prompt is still in flight. |
| `tool_decision` | `sessionID`, `toolUseID`, `decision: "allow" \| "deny"` | Resolve a pending tool-use approval request raised by the PreToolUse hook. |
| `interrupt` | `sessionID` | SIGINT the in-flight claude -p (user clicked Stop). |

New `OutMsg` (server → client):

| Type | Fields | Semantics |
|---|---|---|
| `claude_event` | `sessionID`, `kind`, `payload` | One parsed stream-json event. `kind` is one of `system`, `text_delta`, `text_block_end`, `tool_use_start`, `tool_use_end`, `message_delta` (usage), `message_stop`, `result`. `payload` carries the relevant subset of the source JSON. |
| `tool_approval_request` | `sessionID`, `toolUseID`, `tool`, `input`, `display: { title, summary, danger }` | Claude wants to run a tool; frontend shows the Approve/Deny card. The PreToolUse hook is BLOCKED in the pod until `tool_decision` arrives. |
| `claude_error` | `sessionID`, `code`, `message` | Spawn failed, parse failed, claude exited non-zero with no result event, etc. |

The TUI renderer reuses V0 frames unchanged (`pty_data`, `stdin`).
No mid-conversation renderer switching means no `set_renderer` frame.

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

### 6.2 Start Claude dialog

New component `StartClaudeDialog.tsx`. Triggered by the user typing
`claude` / `/claude` in the chat composer, OR by clicking the
header's existing "Claude" button. Two radio buttons (TUI / UI),
default selected to UI, Cancel / Start. On Start, the WS sends
`enter_claude { renderer }`. The component then unmounts; the view
that mounts next is driven by the `claude_entered` response from the
server.

### 6.3 Header (no renderer toggle)

While in Claude mode the header just shows the existing "Exit
Claude" button, plus the existing Stop affordance from V0 (which
only applies to UI renderer's in-flight prompt).

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

- Modified `enter_claude` handler: now requires `renderer` field on
  the inbound frame. Persists `renderer` on the session metadata.
  - `renderer == "tui"`: runs the V0 path verbatim (TmuxShell
    EnterClaude → send-keys `claude --resume <uuid>` if uuid is
    known, else just `claude`). Emits `claude_entered { renderer:
    "tui" }`.
  - `renderer == "ui"`: does NOT send-keys `claude` into the pane.
    The pane stays at the bash prompt; we'll spawn `claude -p` on
    demand per prompt (§3.1). Emits `claude_entered { renderer:
    "ui" }`.
- Modified `exit_claude` handler: dispatches by renderer.
  - "tui": existing V0 path (kill bash + respawn).
  - "ui": if `inFlightPrompt`, SIGINT the claude -p; otherwise no-op.
    Then clear renderer in metadata. Emit `claude_exited`.
- New `claude_prompt`: only valid when renderer == "ui". Launch via
  `claude.Runner.Prompt(sessionID, prompt)`, forward each parsed
  event as a `claude_event` frame. Mark `inFlightPrompt = true`
  while the process runs.
- New `tool_decision`: call `Manager.ResolveTool(toolUseID, decision)`.
- New `interrupt`: SIGINT the in-flight `claude -p` PID. The
  Runner's reader goroutine sees EOF, finishes flushing buffered
  events, and we mark `inFlightPrompt = false`.

V0's `pty_data` / `stdin` paths are reached only when renderer is
"tui" — the existing code is unchanged.

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
3. **Phase 3**: WS protocol — modify `enter_claude` to require
   `renderer`; add `claude_prompt`, `tool_decision`, `interrupt`
   inbound; add `claude_event`, `tool_approval_request`,
   `claude_error` outbound. `Manager` gains per-session `Renderer`
   and `ClaudeSessionID` (UUID). `idle`/`reattach`/`claude_entered`
   carry the renderer.
4. **Phase 4**: React. `StartClaudeDialog` modal. UI-renderer:
   `ClaudeChatView` skeleton renders incoming `claude_event` frames
   as raw text blocks (no Markdown yet). Composer textarea + Stop.
   Tool calls show as plain "Allow / Deny" buttons.
5. **Phase 5**: rich rendering + token/cost footer + edge cases.
   react-markdown + remark-gfm; syntax-highlighted code; collapsed
   thinking blocks; styled tool-call cards. Token counts from
   `message_delta.usage` shown in a footer with client-side dollar
   estimate. Pod-restart replay tested. CONTEXT.md + README.

Estimate: 12–18 hours focused work, 2–3 calendar days.

(Was 7 phases in the previous draft. Phase 6's renderer toggle is
gone because §2.2 locks the renderer at entry. Other phases
collapsed into Phase 5 for the same reason.)

---

## 10. Acceptance

Done when:
- Type `claude` in a session → Start Claude dialog opens; pick UI;
  the chat view appears with empty history.
- Type a question → tokens stream in as Markdown.
- Claude asks to run `bash: ls` → card appears → user clicks Allow →
  result appears under the card → Claude continues.
- Every tool use yields a card; Deny → tool is skipped; Claude sees
  a "user denied" message and adjusts.
- Click Exit Claude → return to shell. Type `claude` again → dialog
  appears; pick TUI; xterm.js shows the same conversation
  continuing (proves `--resume <uuid>` works across re-entries and
  across renderers).
- Two Alfred sessions running Claude simultaneously (one UI, one
  TUI) work independently; clicking back and forth in the sidebar
  doesn't interrupt either.
- Pod restart (`kubectl rollout`): re-enter Claude, see the full
  prior conversation history (from `--resume`).
- Existing `/claude` TUI flow unaffected — all existing 17 E2E pass.
