# Claude UI Mode — Design Spec

> Status: draft 2026-06-13. Extends the Claude-mode TUI feature shipped on
> 2026-06-13 (`docs/superpowers/specs/2026-06-13-claude-mode-design.md`).

## 1. Goal

The existing **Claude mode** (xterm.js + raw PTY passthrough) drops the
user into Claude's native TUI. It works but loses the "Alfred-shaped"
UX — no Markdown rendering, no inline diff cards, no permission-prompt
UI, no Tool-use approval flow integrated with the rest of the workbench.

This spec adds a **second view of the same Claude conversation**:

- A ChatGPT-style **chat UI** rendered by Alfred (the "UI mode").
- A toggle in the header lets the user switch between TUI and UI **without
  losing the conversation**. Both views are backed by the same
  `claude --session-id <uuid>` transcript on the PVC.
- Tool use (bash, edit, write, etc.) is **intercepted by Alfred** — Claude
  asks, the React UI shows an "Approve / Deny" card, Alfred runs the tool
  (or refuses), result goes back to Claude.

This is **not** a replacement for the TUI. Users who like the native TUI
keep it. Users who prefer Alfred-rendered chat get a parallel path.

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
1. Alfred session with mode=shell, user pokes around.
2. User types `claude` (or clicks Claude button) → enters Claude-mode TUI
   (existing behavior). xterm.js renders Claude's native screen.
3. NEW: header now shows three buttons: "TUI" (highlighted), "UI", "Exit Claude".
4. User clicks "UI" → frontend switches view to chat-style ChatStream,
   backend swaps the underlying `claude` process from interactive TUI
   to `claude -p --output-format stream-json --resume <uuid>`. Same UUID.
   Same on-disk transcript. Claude doesn't know it's "the same user
   different view" — it just sees a new -p invocation with --resume.
5. User types a prompt in the chat input → backend spawns ONE
   `claude -p` process with the prompt → streams events back via WS
   pty_data-shaped frames (but typed as claude_event) → React renders.
6. Claude wants to run `bash: ls /` → PreToolUse hook → POSTs to
   localhost:claude-bridge → Alfred backend → WS push to React:
   "Claude wants to run bash: ls /. [Allow] [Deny] [Allow always]".
   React resolves the prompt → backend returns to hook → hook returns
   allow/deny to Claude → Claude proceeds (or doesn't).
7. User clicks "TUI" → backend kills the current `claude -p` (if one is
   running), respawns interactive `claude --resume <uuid>` in the tmux
   pane, frontend switches back to xterm.js view.
8. User clicks "Exit Claude" → existing flow. Both processes die,
   tmux pane respawns bash.
```

### 2.1 cwd / env

`claude` is always invoked from the session's tmux pane (same as today).
So whatever cwd the user navigated to in shell mode before entering
Claude is what Claude sees. This is the same property the TUI has, and
also satisfies the SDK's requirement that `--resume <uuid>` runs in the
**same project directory** as where the session was originated.

### 2.2 Switching views mid-tool-use

If the user clicks TUI while Claude is mid-stream (writing tokens, mid-
tool-call), we let the current turn finish first then switch on the
next idle:

- **UI → TUI mid-stream**: refuse the switch with a toast "Wait for
  Claude to finish this turn". The TUI button is disabled while
  `inFlight=true`.
- **TUI → UI mid-stream**: same. We can't read TUI's internal state
  to know it's mid-tool-call, but we approximate via a "send anything
  to Claude" inhibit: the user must click `/exit` -> wait for prompt
  -> then UI toggle is enabled.

This avoids the bad case where switching kills Claude mid-call and
leaves a half-finished tool execution.

---

## 3. Architecture: process model

### 3.1 Two processes never run at the same time

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

New `InMsg` types (client → server):

| Type | Fields | Semantics |
|---|---|---|
| `enter_claude_ui` | `sessionID` | Switch from shell or TUI-claude to UI claude. If TUI-claude was running, kill it first. |
| `exit_claude_ui` | `sessionID` | Leave UI mode; if a prompt is in flight, refuse. |
| `switch_to_tui` | `sessionID` | UI → TUI switch. Refused if `inFlight`. |
| `switch_to_ui` | `sessionID` | TUI → UI switch. Refused if TUI is mid-tool-call (best-effort detected). |
| `claude_prompt` | `sessionID`, `text` | Send one prompt in UI mode. Spawns claude -p. |
| `tool_decision` | `sessionID`, `toolUseID`, `decision: "allow"\|"deny"`, `rememberFor?: string` | Resolve a pending tool-use approval request. |
| `interrupt` | `sessionID` | Kill the current claude -p (user clicked "Stop"). Cleans up the on-disk lock. |

New `OutMsg` types (server → client):

| Type | Fields | Semantics |
|---|---|---|
| `claude_ui_entered` | `sessionID`, `convoID` | UI mode active. Frontend mounts the new view. |
| `claude_ui_exited` | `sessionID` | Back to shell. |
| `claude_event` | `sessionID`, `kind`, `payload` | One parsed stream-json event. `kind` is one of `system`, `text_delta`, `text_block_end`, `tool_use_start`, `tool_use_end`, `message_delta` (usage), `message_stop`, `result`. `payload` carries the relevant subset of the source event. |
| `tool_approval_request` | `sessionID`, `toolUseID`, `tool`, `input`, `display: { title, summary, danger }` | Claude wants to run a tool; the frontend shows an Allow/Deny card. The backend BLOCKS Claude's process (via the PreToolUse hook) until `tool_decision` comes back. |
| `claude_error` | `sessionID`, `code`, `message` | Spawn failed, parse failed, claude exited non-zero with no result event, etc. |

The existing `idle` / `reattach` gain another mode value:

```
"mode": "shell" | "claude_tui" | "claude_ui"
```

Old `claude_entered` / `claude_exited` are kept (they're the TUI path)
but the new UI flow uses the more granular events above.

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

### 5.2 "Allow always" remembering

When the user clicks "Allow always for `bash`":

- Frontend includes `rememberFor: "bash"` in the `tool_decision` frame.
- Backend appends to `~/.claude/settings.json`:
  ```json
  { "permissions": { "allow": ["Bash(*)"] } }
  ```
  (Claude reads this on the next hook invocation and pre-approves.)

This survives Pod restarts.

### 5.3 Disallowed tools (v1.5)

Out of scope for v1: letting the user pre-blacklist tools. Default is to
ask every time, with the "Allow always" path to amortize.

---

## 6. Frontend changes

### 6.1 New `web/src/features/claude-ui/` package

- `ClaudeChatView.tsx`: the main view; renders the chat history,
  composer, tool-approval cards, usage footer.
- `ClaudeMessageRenderer.tsx`: takes the accumulated event list for a
  turn and renders Markdown (assistant text), code blocks with syntax
  highlight (we already have a renderer for this in ChatStream — share
  it), thinking blocks (collapsed by default), tool_use cards.
- `ToolApprovalCard.tsx`: the "Allow / Deny / Allow always" modal/
  card.
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
- `enter_claude_ui`: kill TUI claude if running (via existing
  `ExitClaude`), persist `mode = claude_ui`, write `claude_ui_entered`.
- `claude_prompt`: launch via `claude.Runner.Prompt(...)`, forward
  each event as a `claude_event` frame.
- `tool_decision`: call `Manager.ResolveTool`.
- `switch_to_tui` / `switch_to_ui`: refuse if inFlight; otherwise
  flip the mode flag and (for TUI) re-send `claude --resume <id>` via
  `EnterClaude`.

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
   to the claude -p process. Claude SDK's behavior on SIGINT in
   stream-json mode is undocumented; assume it exits with a non-zero
   code. We treat it as if the turn ended with an error. The hook may
   or may not return before SIGINT propagates — we don't care, the
   user wanted out.

6. **claude --resume on a stale cwd**: if the user shell-mode `cd`'d
   away from the cwd where the conversation was originated, `--resume`
   fails (per SDK docs §4). Mitigation: store cwd at convo-creation
   time, `cd` back before each `claude -p` invocation. Or: refuse to
   enter UI mode if cwd has drifted. v1: refuse, surface a clear error.

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

1. **Phase 1**: backend `internal/claude/runner.go` (spawn + parse
   stream-json), exposed via a smoke endpoint or test. No UI yet.
2. **Phase 2**: localhost bridge for tool intercept, configured via
   settings.json. Demo: a manual `curl` proves "Claude waited, decision
   came back, Claude resumed".
3. **Phase 3**: WS protocol additions; UI mode entry/exit; React
   `ClaudeChatView` skeleton that renders raw events.
4. **Phase 4**: Markdown / code block / thinking rendering.
5. **Phase 5**: Tool approval card UI.
6. **Phase 6**: header "TUI / UI / Exit Claude" 3-way toggle.
7. **Phase 7**: usage footer (token count + dollar estimate).
8. **Phase 8**: edge-case hardening (cwd drift, hook timeout UX,
   stop button), CONTEXT.md + README docs.

Estimate: 15–25 hours focused work, 3–5 calendar days.

---

## 10. Acceptance

Done when:
- User clicks "UI" button in claude mode → sees Markdown-rendered
  chat. Types a question → tokens stream in.
- Claude asks to run `bash: ls` → card appears → user clicks Allow →
  result appears under the card → Claude continues.
- "Allow always for `bash`" persists across page refresh (settings.json
  on PVC).
- Click "TUI" → back to xterm.js → conversation continuation visible
  in TUI (proves `--resume <uuid>` works across modes).
- Pod restart (`kubectl rollout`) preserves transcript: re-enter UI,
  see full history.
- Existing `/claude` TUI flow unaffected — all existing 17 E2E pass.
