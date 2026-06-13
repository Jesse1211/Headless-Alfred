# Claude Mode — Design Spec

> Status: draft 2026-06-13. Supersedes nothing; extends the
> multi-session tmux architecture from
> [`2026-06-11-multi-session-tmux-design.md`](2026-06-11-multi-session-tmux-design.md).

## 1. Goal

Let the user **toggle a running shell session into "Claude mode"** —
which spawns the `claude` CLI inside the same tmux session, inheriting
the current cwd / env / files. The browser switches from chat-stream
rendering to a real terminal emulator; keystrokes go straight to
Claude's stdin; Claude's full TUI (colors, box-drawing, status line,
menus) renders correctly. The user `/exit`s Claude or clicks "Exit
Claude" → control returns to bash in the same session at the same cwd.

This is **the second interaction mode** of the product. Mode 1
(existing) is the ChatGPT-style request/response chat over wrapped
commands. Mode 2 (this) is a Bidirectional PTY stream for full TUI
applications. The product *is* a shell + AI workbench.

### 1.1 Non-goals

- Running multiple Claude instances concurrently per session. One bash
  + one Claude per session, just like one bash + one user command at a
  time today.
- Claude-mode survival across Pod restart. Same trade-off as
  multi-session: tmux dies with the pod; Claude session resets. ~~Go
  process restart, however, MUST preserve a running Claude — same
  guarantee shell mode already gives.~~
- General TUI support (vim, htop, less, …). Those are out of scope as
  a first-class mode; they may work inside Claude mode if Claude
  spawns them, but the chat-stream mode still rejects them implicitly.
- OAuth-based Claude authentication. v1 uses API key only.

---

## 2. User flow

```
1. User creates / picks an existing shell session.
2. User does shell things: `git clone …`, `cd repo`, `npm install`, …
3. User types `/claude` in CommandInput.
   - Frontend sees the `/claude` slash command, emits an `enter_claude`
     WS frame for this session.
   - Backend send-keys `claude\n` into the tmux pane (with a back-
     sentinel suffix, see §4.3), flips this session's mode flag to
     `claude`. The pane is still inside bash — bash will execute
     `claude` as a normal child process.
4. Frontend swaps the chat-stream view for an xterm.js terminal,
   bound to incoming pty_data frames and outgoing stdin frames.
5. User interacts with Claude normally. Arrow keys, Enter, /-commands,
   permission prompts — all work because they're raw stdin bytes.
6. User types `/exit` inside Claude (or Claude crashes / is killed).
   - bash regains the pane, then runs the back-sentinel echo we
     queued at step 3 (e.g. `printf '\\x1eALFRED_BACK_<nonce>'`).
   - Backend's pty parser sees the back-sentinel, flips mode to
     `shell`, emits a `claude_exited` WS frame.
   - Frontend switches back to chat view.
7. cwd / env are exactly where they were before `/claude`. User
   continues normal shell ops.
```

### 2.1 cwd / env preservation across Claude

`claude` runs as a child of the session's bash. Per Unix process
semantics, a child can change its OWN cwd and env, but cannot mutate
the parent's. So:

- Whatever directory the user was in when they typed `/claude` is
  the directory Claude sees on startup.
- Anything Claude does to its cwd / env is local to the Claude
  process and dies with it.
- Files Claude creates, edits, or deletes on the filesystem persist
  (the FS is shared across sessions per spec §1 of the multi-session
  doc).
- When Claude exits, bash's cwd is exactly what it was — the user
  resumes shell-mode work at the same prompt position.

### 2.2 Alternative path: "Exit Claude" button

The header (or wherever we put the chat/claude toggle) shows an
**"Exit Claude"** button while in claude mode.

- Click while Claude is alive: do **not** kill Claude. Show a toast:
  *"Claude is still running. Type `/exit` inside the terminal, or
  click Force Stop."* (We don't unilaterally SIGKILL Claude — it has
  in-flight tool calls and may corrupt state.)
- Click while Claude is already dead but the frontend hadn't received
  the `claude_exited` frame yet (race): just switch the view; the
  frame arrives shortly anyway.
- The "Force Stop" link in the toast triggers a separate
  `force_exit_claude` frame which DOES SIGKILL Claude. Use sparingly.

---

## 3. Architecture: WHY tmux, not raw PTY

The previous spec (§3) chose tmux because:
- tmux outlives the alfred-server process → Go restart preserves
  running commands.
- One tmux session per logical alfred session → process isolation.

Claude mode is **a TUI program running inside an existing tmux
pane**. It is `bash` in the pane → `bash` execs `claude` → `claude`
takes over the pane's PTY → `claude` writes ANSI bytes → tmux's
pipe-pane captures those bytes → alfred-server forwards them to the
browser.

We get for free:
- Pane resize via `tmux send-keys` / pane resize commands.
- Process isolation per session (one Claude per tmux session).
- Go restart preservation (tmux daemon keeps Claude alive across
  alfred-server kills — same mechanism as command preservation in
  shell mode).
- No new PTY plumbing in Go. We already have stream-reader, pipe-
  pane, send-keys, etc.

The only cost is **sentinel parser disambiguation** — see §4.

### 3.1 What "mode" actually controls

For a given session, `mode` is a flag (`shell | claude`) held by
`Manager`. New sessions default to `shell`. The flag changes four
things:

| In shell mode | In claude mode |
|---|---|
| pipe-pane bytes → SentinelParser watches START/END/BACK → `started`/`chunk`/`done` frames | pipe-pane bytes → SentinelParser watches BACK only (START/END ignored) → raw bytes → `pty_data` frames, base64-wrapped |
| Inbound `run` frame → wrap command, send-keys, append `printf END` | Inbound `run` frame → rejected (`mode_mismatch`); user should be using `stdin` frame |
| Inbound `stdin` frame → rejected (`mode_mismatch`) | Inbound `stdin` frame → `tmux send-keys -t <pane> -l <bytes>` |
| Stop button → SIGKILL bash + respawn | Stop button → not shown; use Exit Claude / Force Stop instead |

Crucially, the SentinelParser **never** stops scanning. In claude
mode it just becomes selective about which token classes it acts on
— see §4.7. Claude's TUI output may incidentally contain bytes that
match a START or END token shape; the parser must not flip
`currentCmd` state in claude mode.

#### 3.1.1 Mode persistence

`mode` IS persisted in `sessions.json` (a new field per session).
The reasoning:

- **Go process restart** (e.g. `kubectl rollout`): tmux pane is
  alive, possibly with a running Claude. The new alfred-server
  reads `sessions.json`, sees `mode: claude`, immediately wires the
  WS handler's claude-mode branch for that session. If it didn't,
  the new server would default to shell mode, run the sentinel
  parser against Claude's TUI bytes, and produce nonsense
  `started`/`chunk` frames.
- **Pod restart**: tmux dies, Claude dies. `Manager.Reconcile()`
  rebuilds the session with mode reset to `shell`. The persisted
  `mode: claude` is overwritten back to `shell` as part of
  reconcile's "stored \\ live" branch. A client reconnecting sees
  `mode: shell` in the next `idle`/`reattach` frame and snaps back
  to the chat view.
- **`mode` field migration**: older sessions.json without the field
  parse as `mode: ""`; the loader treats empty as `shell`.

---

## 4. Mode transitions

### 4.1 Enter

```
1. Client → server:   { type: "enter_claude", sessionID }
2. Server:
   - Reject if mode == claude already (already_in_claude error).
   - Reject if currentCmd != nil (session_busy error, §4.6).
   - Generate back-nonce (independent of the start/end sentinel
     nonce; a random 16-hex-byte token).
   - sh.EnterClaude(backNonce):
     - Register backNonce on the SentinelParser. (Parser keeps
       scanning all bytes; in claude mode it ignores START/END
       and acts on BACK. See §4.7.)
     - Send-keys (no Write() / no currentCmd; see §4.8):
         claude; printf '\x1eALFRED_BACK_<nonce>X'
       then SendEnter.
       (The trailing X disambiguates the sentinel exactly like the
        existing START/END parser does — see CONTEXT.md non-obvious
        trap row.)
   - Manager.SetMode(sessionID, "claude") — persists.
   - Server → client: { type: "claude_entered", sessionID }
   - WS handler registers itself as activeStdinClient for this
     session (§10.11).
```

The back-sentinel goes onto the bash command line ahead of time. If
Claude exits cleanly, bash runs the `printf`. If Claude crashes, bash
recovers the prompt and the printf still runs (it's part of the same
compound command).

### 4.2 Inside claude mode

```
Client → server:   { type: "stdin", sessionID, data: base64(bytes) }
Server: tmux send-keys -t <pane> -l <decoded bytes>

Server → client:   { type: "pty_data", sessionID, data: base64(bytes) }
The bytes are whatever pipe-pane captured since the last forward.
Forward in the same chunking as the existing reader (typically up to
8 KiB per chunk).
```

Resize:
```
Client → server:  { type: "resize", sessionID, cols: N, rows: M }
Server: tmux resize-window -t <pane> -x N -y M
(tmux propagates the SIGWINCH to claude automatically)
```

### 4.3 Exit (natural, /exit or Ctrl+D)

```
1. Claude exits → bash regains shell.
2. bash runs the trailing `printf '\x1eALFRED_BACK_<nonce>X'`.
3. Pipe-pane bytes still flowing — parser detects the back-sentinel,
   emits an internal BackEvent.
4. ws.go on BackEvent: mode := shell; persist; write { type: "claude_exited", sessionID }
5. Subsequent pipe-pane bytes go BACK into the SentinelParser. (Note:
   there's a brief window of bytes between Claude exiting and the
   back-sentinel firing — those are mostly bash's prompt-restore
   sequences. They land in stateOutside of the parser and are
   discarded — same mechanism as `stty -echo` lines today.)
```

### 4.4 Exit (force stop)

```
Client → server:   { type: "force_exit_claude", sessionID }
Server: send-keys C-c C-c (try graceful first), wait 500ms,
        if still alive send-keys C-d, wait 500ms,
        if still alive: kill the pane's foreground PID (using pgrep on
        the pane's PID hierarchy). bash then runs the back-sentinel
        printf and we transition normally.
```

### 4.5 Race: client sends `enter_claude` twice

Server returns `error { code: "already_in_claude" }` for the second
one. Client should disable the /claude slash while mode is claude.

### 4.6 Race: client `enter_claude` while a command is running

Reject with `error { code: "session_busy" }`. The client should first
let the command finish or click Stop.

### 4.7 SentinelParser state machine, both modes

Today's parser (shell mode) has two top-level states: `Outside`,
`Inside` (a wrapped command). On entering `Inside`, it accumulates
bytes for the current cmdID's buffer and emits `chunk` events; on
exiting via END, it emits `Ended`.

Claude mode adds **one** new dimension: which token classes the
parser *acts on*. The byte-by-byte scan keeps running unchanged
(state never gets out of sync), but token-fire callbacks become
mode-conditional:

| Token observed | Parser action in shell mode | Parser action in claude mode |
|---|---|---|
| `\x1eALFRED_START_<nonce>X` | enter Inside, emit Started | **ignored** (no state change; bytes pass through to the pty_data forwarder) |
| `\x1eALFRED_END_<nonce>X` | exit Inside, emit Ended | **ignored** |
| `\x1eALFRED_BACK_<nonce>X` | **ignored** (no use for it in shell mode) | emit BackEvent, ws.go flips mode → shell |
| any other byte | depends on Inside/Outside state | forwarded as `pty_data` |

Note that we register a **per-session BACK nonce** at EnterClaude
time. Each Claude entry uses a fresh BACK nonce that's discarded
when mode flips back to shell — so a stale BACK nonce from a
previous Claude entry cannot fire mid-shell-command.

### 4.8 EnterClaude bypasses the Write() / currentCmd API

Important implementation contract: `TmuxShell.EnterClaude(nonce)`
does **not** call the same `Write()` path used for normal user
commands. Specifically it MUST NOT:

- create a `RunningCommand` record on `currentCmd`
- write a `commands/<cmdID>.json` to the store
- emit a `started` event
- be subject to the `ErrBusy` check (we already checked busy at the
  WS-handler level in §4.6)

It only does two things:
1. Register the back-nonce on the parser (claude-mode listener).
2. `runner.SendText(sessionID, "claude; printf '\x1eALFRED_BACK_<nonce>X'")` +
   `runner.SendEnter(sessionID)`.

Symmetrically, the parser BackEvent does not touch `currentCmd`
state — there was never a record for the `claude` command, so
there's nothing to clean up.

---

## 5. WS protocol additions

New `InMsg` types (from client):

| `type` | fields | semantics |
|---|---|---|
| `enter_claude` | `sessionID` | switch to claude mode |
| `exit_claude` | `sessionID` | user clicked Exit Claude button — soft-attempt (see §4.3 toast logic) |
| `force_exit_claude` | `sessionID` | SIGINT/SIGKILL Claude |
| `stdin` | `sessionID`, `data` (b64) | forward raw bytes to PTY |
| `resize` | `sessionID`, `cols`, `rows` | TIOCSWINSZ |

New `OutMsg` types (from server):

| `type` | fields | semantics |
|---|---|---|
| `claude_entered` | `sessionID` | mode is now claude |
| `claude_exited` | `sessionID` | mode is now shell |
| `pty_data` | `sessionID`, `data` (b64) | raw bytes for xterm.write |
| `pty_replay` | `sessionID`, `data` (b64) | bulk historical bytes for a reattaching client to repopulate the terminal; sent ONCE immediately after `reattach` when the reattaching session is in claude mode (§10.8) |

The existing frames (`started`/`chunk`/`done`/`error`/`idle`/`reattach`
/`session_closed`/`session_renamed`) are unchanged in shape EXCEPT
both `idle` and `reattach` gain a **required** `mode: "shell" |
"claude"` field. The client uses this to mount the correct view on
connect without race. For sessions where mode=claude, the server
follows the `reattach` frame with a `pty_replay` frame (~256 KiB of
trailing pipe-pane bytes) so xterm.js can repopulate, then begins
streaming live `pty_data`.

---

## 6. Backend changes

### 6.1 `internal/session/`

- Add `Mode` to `SessionMeta` (so it's persisted in `sessions.json`).
  JSON tag `"mode,omitempty"`; loader treats `""` as `shell`.
- `Manager.Create()` initializes `mode: "shell"`.
- `Manager.SetMode(sessionID, mode)`: sets in-memory, then
  `persistMetas()` atomically writes the new sessions.json.
- `Manager.GetMode(sessionID)`.
- `Manager.Reconcile()`: in the "stored \\ live" branch (tmux was
  dead, pane recreated), force-reset the session's mode to `shell`
  before persisting — because the running Claude was killed with
  the pod and the new bash is back at the prompt.
- Add a single-active-stdin-client registry: per session, an
  `activeStdinWS *something` reference held by Manager. Methods
  `ClaimStdin(sid, ws) bool` (returns false if already claimed by
  another), `ReleaseStdin(sid, ws)` (called when the ws disconnects
  or mode → shell). See §10.11.

### 6.2 `internal/shell/tmux_shell.go`

- Add `TmuxShell.EnterClaude(nonce)` → send-keys the compound command.
- Add `TmuxShell.SendRaw(bytes)` → wraps `runner.SendText` minus the
  Enter (used for stdin forwarding).
- Add `TmuxShell.Resize(cols, rows)` → `runner.ResizeWindow(...)`.
- SentinelParser gets a `BackSentinel(nonce)` registration method that
  matches a third token pattern in addition to START/END. When the
  parser hits BackSentinel, it emits a `BackEvent` (struct), which
  flows through the broadcaster.

### 6.3 `internal/api/`

- `wsproto.go`: extend `InMsg` / `OutMsg` with the fields above.
- `ws.go`: dispatch `enter_claude` / `stdin` / etc. through a new
  `handleClaudeInbound`. Outbound: when manager mode==claude, the
  pipe-pane → fan-in chunks become `pty_data` frames instead of
  `chunk`/`started`/`done`. BackEvent → mode reset + `claude_exited`.
- `anthropic_key.go`: new handler — `POST /api/anthropic-key`, body
  `{ "key": "sk-..." }`. Writes `~/.claude/.credentials.json` (or
  whatever claude code expects, TBC on first integration test). Same
  shape as `git_credentials.go`.

### 6.4 `internal/shell/tmuxio/runner.go`

- Add `Runner.ResizeWindow(session, cols, rows int) error` →
  `tmux resize-window -t <session> -x <cols> -y <rows>`. Mirrored in
  `FakeRunner`.

---

## 7. Frontend changes

### 7.1 New package `web/src/features/claude/`

- `ClaudeTerminal.tsx`: wraps xterm.js. Receives `pty_data` frames
  via a prop callback registered with `useSessions` and writes to the
  terminal. Captures keyboard input and emits `stdin` frames. Uses
  `xterm-addon-fit` to size to its container; on resize, emits a
  `resize` frame.
- `ClaudeTerminal.css`: dark theme matching the rest of the app.

### 7.2 `web/src/features/sessions/useSessions.ts`

- Add `mode: 'shell' | 'claude'` to `PerSessionState`.
- `onMessage` reducer:
  - `claude_entered` → set mode=claude
  - `claude_exited` → set mode=shell
  - `pty_data` → push bytes onto a per-session `ptyBuffer` (a ref'd
    Uint8Array queue, NOT React state — performance matters here)
- Add `enterClaude(sid)`, `exitClaude(sid)`, `forceExitClaude(sid)`,
  `sendStdin(sid, bytes)`, `sendResize(sid, cols, rows)` callbacks.

### 7.3 `web/src/features/sessions/WorkspacePage.tsx`

- If `ps.mode === 'claude'`: render `<ClaudeTerminal sessionID=… />`
  + Exit Claude button. Hide ChatStream + CommandInput.
- Else: render existing ChatStream + CommandInput. Inside
  CommandInput, intercept `/claude` as a slash command and trigger
  `enterClaude(sid)`.

### 7.4 `web/src/lib/ws.ts`

- Extend the discriminated unions with the new types.

### 7.5 `web/src/lib/api.ts`

- Add `saveAnthropicKey({ key })`.

### 7.6 Header gear menu

- Already has "Git credentials". Add a second item: "Anthropic API
  key" → opens a new modal (`AnthropicKeyDialog.tsx`) similar to
  `GitCredentialsDialog`.

---

## 8. Container changes

### 8.1 Dockerfile

The runtime stage gets a "developer toolkit" + Node + Claude code:

```dockerfile
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates \
      git openssh-client \
      curl wget iputils-ping dnsutils \
      vim-tiny nano less jq unzip xz-utils \
      procps tini tmux \
 && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && npm install -g @anthropic-ai/claude-code \
 && apt-get clean && rm -rf /var/lib/apt/lists/* \
 && useradd -m -u 1000 -s /bin/bash alfred \
 && mkdir -p /data \
 && chown alfred:alfred /data
```

Image size: ~200 MB → ~450 MB (Node alone is ~120 MB). Acceptable.

### 8.2 `deploy/manifests/deployment.yaml`

No change. The Anthropic API key is set via the new REST endpoint at
runtime, persisted on the PVC; not a K8s Secret. (Future: we could
add an ANTHROPIC_API_KEY env var as a fallback that the REST
endpoint reads if no key has been set yet — out of scope v1.)

---

## 9. Tests

### 9.1 Unit

- `internal/shell/tmux_shell_test.go`: EnterClaude sends the compound
  command, SendRaw works, BackSentinel parser detection.
- `internal/api/git_credentials_test.go`-style: `anthropic_key_test.go`
  validates the new handler.

### 9.2 E2E

- `TestE2E_ClaudeMode_EnterRunCommandExit`: enter claude, send
  `echo hi` as stdin keystrokes, see `pty_data` containing 'hi',
  send `/exit\n`, see `claude_exited`. (Without actually invoking
  Anthropic — we can use a stub script `/usr/local/bin/claude` for
  this test, OR we can mock the model call. TBD: simplest is a
  fake-claude shell script in the E2E image.)
- The existing 17 E2E scenarios must continue to pass — no
  regression.

### 9.3 Smoke

- `scripts/claude-mode-smoke/main.go`: hits the WS, enters claude,
  sends a simple stdin, asserts pty_data, exits. Headless equivalent
  of the manual flow.

---

## 10. Edge cases and open questions

1. **Claude doesn't start (no API key set)**: Claude prints an error
   and exits immediately. The back-sentinel still fires, mode reverts
   to shell, the user sees claude's stderr in the brief terminal
   view, then is back in chat mode. Acceptable. UI may want to
   detect rapid exit and surface "did you set your API key?"
   guidance.

2. **Network egress blocked**: same as above.

3. **claude command not found** (Dockerfile broke): bash prints
   `claude: command not found`, fires back-sentinel, mode reverts.
   The terminal view shows "claude: command not found", user is back
   in chat. Acceptable.

4. **Frontend mounts terminal but no `pty_data` yet**: xterm shows a
   blank black box for ~200ms. Acceptable.

5. **`/claude` typed mid-command**: covered by §4.6 — reject.

6. **stdin with control chars (Ctrl+C, Ctrl+D, arrow keys)**: these
   are just bytes (`\x03`, `\x04`, `\x1b[A` etc.) The browser captures
   them via xterm.js's `onData` callback, sends as `stdin` frames,
   the backend send-keys -l forwards them. Tested by Claude itself
   needing arrow keys for menu navigation.

7. **Back-sentinel collides with claude's own output**: claude is
   unlikely to print our 36-byte unique nonce sequence verbatim, but
   if it ever did (e.g. user pastes our sentinel into claude on
   purpose) the parser would prematurely flip mode. Mitigated by the
   nonce being per-session and 16 random hex bytes (~64 bits of
   entropy). Same threat model as the existing START/END sentinels.

8. **Mode persistence across WS reconnect**: when the browser
   reconnects, it must learn the current mode of each session. The
   `idle` and `reattach` frames carry a `mode` field. For sessions
   already in claude mode at reconnect time, the server also pushes
   the most recent ~256 KiB of pty.stream bytes as a `pty_replay`
   frame immediately after `reattach`. xterm.js consumes the replay
   bytes as if they had streamed live; ANSI cursor-positioning
   sequences re-establish the screen state. (Claude redraws fully
   on most user input, so the post-replay terminal looks correct
   after the next keystroke even if the replay was mid-frame.)

9. **Cursor / screen state during reconnect**: as an extra safety
   net, after sending the pty_replay the server send-keys a single
   ASCII NUL (`\x00`) into Claude's stdin. NUL is a no-op for Claude
   itself, but Claude's input loop typically schedules a screen
   redraw on any input event — so the new client sees a fresh full
   frame within ~100ms of reconnecting, even if mid-prompt.

10. **xterm.js bundle size**: ~250 KB minified. Acceptable for a tool
    used by one person who already accepts ~75 KB gzip JS.

11. **Multi-tab concurrent stdin**: two browser tabs connected to
    the same session, both in claude mode, both typing — their
    bytes would interleave at the PTY and corrupt Claude's input.
    Resolution: per session, the WS handler tracks an
    `activeStdinClient *wsClient`. The first WS connection to
    receive a `claude_entered` frame for that session becomes
    active. Subsequent WS connections for the same session render
    Claude's terminal in read-only mode: they receive `pty_data`
    frames (so they see what's happening), but their `stdin` and
    `resize` frames are rejected with `error { code:
    "claude_not_active", message: "Drive Claude from the other
    tab, or close that tab" }`. When the active client disconnects,
    the role is released; the next stdin/resize/exit_claude frame
    from any remaining client claims it. The active-client identity
    is NOT persisted — Go restart clears it; any reconnecting tab
    can claim. Active-client role is per-session, so the user can
    drive session A from tab 1 and session B from tab 2.

12. **Shell mode currentCmd state survives a claude excursion**:
    when entering claude, `currentCmd` is required to be nil (§4.6).
    Therefore exiting back to shell, `currentCmd` is still nil — the
    parser's Outside-state byte counter (used for the buffer cap
    accounting in `tmux_shell.go`) is unchanged. No interaction
    between claude mode and the shell-mode StreamTruncateThreshold
    bookkeeping.

---

## 11. Phasing

Execution order (each = 1 commit):

1. Dockerfile: full developer toolkit + Node 22 + claude code.
2. Manually verify (kubectl exec) that `claude` runs in the
   container, accepts API key from env.
3. Backend: SessionMode type + new WS frame definitions.
4. Backend: `EnterClaude` / `SendRaw` / `Resize` on TmuxShell + parser
   BackSentinel.
5. Backend: ws.go mode dispatch — `stdin` → send-keys; `pty_data`
   passthrough in claude mode.
6. Backend: `POST /api/anthropic-key`.
7. Frontend: install xterm.js + ClaudeTerminal component.
8. Frontend: slash command parsing in CommandInput + Exit Claude
   button + mode-aware WorkspacePage routing.
9. Frontend: AnthropicKeyDialog + saveAnthropicKey API helper.
10. E2E + smoke + CONTEXT.md + README "Claude mode" section.

Estimate: ~17–22 hours of focused work, 2–3 calendar days.

---

## 12. Acceptance

Done when:

- Click "/claude" → terminal appears within 1 s → I can `ls`,
  `pwd`, arrow-key through Claude's menu, type a prompt and see
  Claude's streamed response.
- `/exit` returns me to the chat view in the same cwd.
- Refresh the browser mid-Claude → terminal re-mounts with the
  current pty stream.
- `kubectl rollout restart deployment/alfred` while in Claude →
  Claude survives (mode persists via tmux).
- Pod restart → both bash and Claude die (accepted trade-off).
- All 17 existing E2E continue to pass.
- New E2E `TestE2E_ClaudeMode_*` pass.
