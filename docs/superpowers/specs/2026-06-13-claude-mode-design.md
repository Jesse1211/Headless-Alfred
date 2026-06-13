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

### 2.1 Alternative path: "Exit Claude" button

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

For a given session, `mode` is an in-memory flag (`shell | claude`)
held by `Manager`. It changes three things in the WS handler:

| In shell mode | In claude mode |
|---|---|
| pipe-pane bytes → SentinelParser → `started`/`chunk`/`done` frames | pipe-pane bytes → raw → `pty_data` frame, base64-wrapped |
| Inbound `run` frame → wrap command, send-keys, append `printf END` | Inbound `run` frame → rejected (`mode_mismatch`); user should be using `stdin` frame |
| Inbound `stdin` frame → rejected | Inbound `stdin` frame → `tmux send-keys -t <pane> -l <bytes>` |
| Stop button → SIGKILL bash + respawn | Stop button → not shown; use Exit Claude instead |

Mode is NOT persisted to `sessions.json`. Pod restart → tmux gone →
mode resets to shell (whatever bash was doing pre-Claude is gone
anyway).

---

## 4. Mode transitions

### 4.1 Enter

```
1. Client → server:   { type: "enter_claude", sessionID }
2. Server:
   - Reject if mode == claude already (already_in_claude error)
   - Generate back-nonce (independent of the start/end sentinel nonce).
   - sh.EnterClaude(backNonce):
     - SentinelParser stops emitting Started/Ended.
     - Send-keys:  claude; printf '\x1eALFRED_BACK_<nonce>X'\n
       (the trailing X disambiguates the sentinel exactly like the
        existing START/END parser does — see CONTEXT.md non-obvious
        trap row.)
   - Manager marks session mode = claude.
   - Server → client: { type: "claude_entered", sessionID }
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
4. ws.go on BackEvent: mode := shell; write { type: "claude_exited", sessionID }
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

The existing frames (`started`/`chunk`/`done`/`error`/`idle`/`reattach`
/`session_closed`/`session_renamed`) are unchanged. `idle` /
`reattach` SHOULD include a `mode` field so the client knows which
view to mount on connect, though for v1 we accept that a reconnecting
client briefly mounts the wrong view, then corrects on the first
mode-change frame.

---

## 6. Backend changes

### 6.1 `internal/session/`

- Add `Mode` to in-memory `SessionState` (not persisted).
- Add `Manager.SetMode(sessionID, mode)`, `Manager.GetMode(sessionID)`.

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
   reconnects, it must learn the current mode of each session. v1
   solution: extend the `idle` / `reattach` frames to include `mode`.
   v1.5: also include the most recent ~256KB of `pty_data` so the
   terminal can be repopulated on reconnect (similar to
   `outputSoFar` in `reattach` today).

9. **xterm.js bundle size**: ~250 KB minified. Acceptable for a tool
   used by one person who already accepts ~75 KB gzip JS.

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
