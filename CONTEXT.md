# Headless Alfred — Context

This document is for someone (human or LLM) about to work on this codebase. It captures the **why** and the **non-obvious**, not the **what** — the code already tells you the latter.

If you have less than 5 minutes, read just **Mental model** and **Three invariants that don't bend**.

---

## Mental model

One Go process per Pod. That process owns:

1. **One bash subprocess** spawned at startup with `--noprofile --norc` and empty `PS1`. It lives until the Go process dies.
2. **One PTY** that bash's stdin/stdout flow through. Output is parsed via sentinel markers (`\x1eALFRED_START_<nonce> <cmdID> <cwd>\x1e`) wrapped around each user command. **PTY echo is disabled at startup** (`stty -echo`) so the wrapper script we feed bash isn't echoed back into the parser; see the traps table below.
3. **Two broadcasters** (raw bytes + typed events). Subscribers come and go (WebSocket connections, the disk-writer). The bash process never blocks on a slow subscriber.
4. **A JSON file store** at `/data` — one file per command for metadata, one for output. Atomic writes via tmp+rename.
5. **An HTTP+WS surface** for the React client.

The React app is just a viewer. **All state of consequence lives in the Go process and on the PVC.** Close the browser → bash keeps running → reconnect → reattach event delivers the in-memory buffer so far.

---

## Three invariants that don't bend

If you find yourself violating any of these, stop and think.

### 1. Bash lifecycle ≠ WebSocket lifecycle.

Bash is born when Go starts and dies when Go stops. The set of WebSocket connections is orthogonal. If you add code that ties a bash command to a specific connection, you have introduced a regression that breaks the entire premise of the project (long-running jobs surviving a closed laptop).

**Strengthening (multi-session):** Bash lifecycle ≠ Go-process lifecycle either. The tmux server outlives alfred-server. `kubectl rollout` does NOT terminate in-flight commands; the in-flight command keeps streaming into `pty.stream`, and the new alfred-server process resumes parsing it from `pty.offset` and re-emits any pending `Started`/`Ended` events to reconnecting clients. Pod restart DOES terminate them; this is an accepted trade-off documented in spec §1 non-goals.

### 2. Output buffer lives where commands live: in `shell.Shell`.

The `EndedEvent.Output` field carries the buffer at the moment of completion. **Do not** try to read the buffer via `Shell.CurrentCommand()` after a command ends — `currentCmd` is already nil by then. This bit us during Plan 2; the fix was to attach the buffer to the event itself.

### 3. `waitLoop` must atomically mark the shell unavailable.

When bash dies, `waitLoop` must clear `currentCmd`, `available`, `cmd`, `pty` **under the same lock** before releasing it to publish the Ended event. Otherwise a concurrent `Write()` between unlock and the restart's re-lock can sneak in, write to a closed PTY, and brick the shell with an orphan command that no event will ever clear. There's a comment in `internal/shell/shell.go` `waitLoop` explaining this; respect it.

---

## Why these choices

| Choice | Why |
|---|---|
| Single user, hardcoded credentials | Personal tool. Per spec §8: K8s Secret env vars, static long-random token, no JWT/session. Acceptable trade-off given Secret rotation = pod restart. |
| Up to 8 bash sessions, one command at a time per session | Each session is an isolated tmux session (own cwd / env / aliases) but shares the container FS. Concurrency *within* a session is what `&` is for; concurrency *across* sessions is what the sidebar is for. Was originally single-bash (spec §3, since superseded by `docs/superpowers/specs/2026-06-11-multi-session-tmux-design.md`). |
| Output **first** 10 MB kept, not last | When a `npm run train` blows up, the head of output usually contains the actual failure. Simpler than rolling truncation. |
| `OutputPath` removed from `Record` | Was duplicated state. Path is deterministic — `store.outputPath(id)`. Storing it caused a lost-update race between `Save(rec)` and `WriteOutput(id)`. |
| Stop = SIGKILL bash + restart | We tried SIGINT through the PTY — terminal line discipline on macOS dropped queued sentinel printfs and we never got a clean END event. Killing bash is simple, reliable, and produces a non-zero exit code. Trade-off: cwd/env/aliases reset on Stop. |
| Trust `X-Forwarded-For` blindly | Traefik terminates TLS in front; the Service is only reachable through it; XFF is set by Traefik. If you ever expose the Service directly (don't), revisit this. |
| Skip Ingress in kind E2E | Let's Encrypt cannot issue against `localhost`. We port-forward the Service to `127.0.0.1:18080` and test the inner stack. Ingress correctness is verified manually in production. |
| `USER 1000` (numeric) in Dockerfile | `runAsNonRoot: true` cannot verify a user named `alfred` without introspecting `/etc/passwd` inside the image. Use a numeric UID and the orchestrator agrees with the image. |

---

## Non-obvious traps we already fell into

If you change any of these, you will probably hit the same bug we already fixed. Each has a regression test guarding it.

| Trap | What happens if you forget | Test |
|---|---|---|
| `http.FileServer` 301-redirects any path ending in `/index.html` to the parent | SPA routes like `/login` bounce forever | `TestHandler_SPAFallback_DoesNotRedirect` |
| `statusRecorder` (RequestLogger's wrapper) hides the underlying `http.Hijacker` | Every WebSocket upgrade returns 500 with "response does not implement http.Hijacker" | `TestRequestLogger_PreservesHijacker` |
| `go:embed dist` skips dotfiles by default | An empty `dist/` (only `.gitkeep`) reports "no embeddable files" at compile time | The Dockerfile populates `dist/` with the real build; locally use `make embed-web` |
| `printf` with a `\x1e` followed by raw text confuses the parser if the next byte isn't a known terminator | We append a literal `X` after the closing RS to give the parser a definitive end-of-sentinel marker | `TestSentinelParser_HandlesSentinelSplitAcrossFeeds` |
| Login rate limiter is per-IP (`X-Forwarded-For` from Traefik) | Tests that all log in from `127.0.0.1` burn the same bucket | E2E tests set `X-Forwarded-For` per `t.Name()`; production deals with this naturally |
| Bash PTY echo is on by default — every byte written into the master is echoed back through the slave | The echoed wrapper (`printf 'START…'`, the user command, `__alfred_ec=$?`, `printf 'END…'`) arrives **after** the START sentinel has flipped the parser to `stateInside`, so it leaks into the visible output as garbage like `__alfred_ec=$?` and `printf '\\x1eALFRED_END_…'` | `startLocked` writes `stty -echo\n` to the PTY immediately after `pty.Start`; that command's own echo lands in `stateOutside` and is discarded |
| React `<StrictMode>` calls every state updater **twice** in dev to detect non-idempotent side effects | A naive `setMessages((m) => [...m, newOne])` on `done` events appends each completed command twice in the chat stream (server store is correct, only the UI doubles) | `sessionsReducer.ts`'s `done` case checks `cur.messages.some(mm => mm.id === m.cmdId)` and returns the prev Map unchanged on a re-fire — proves idempotency in `sessionsReducer.test.ts` "done is idempotent (StrictMode safe)" |
| `tmux send-keys -l` sends `\n` as a literal character, not Enter | bash never executes the wrapper-script line; sentinel never fires; UI hangs forever waiting for `Started` | `TmuxRunner` splits into `SendText` + `SendEnter`; covered by `TestExecRunner_SendTextThenEnter_ExecutesCommand` |
| `tmux pipe-pane` writer holds `pty.stream` open across a naive rename, so output ends up in the unlinked inode | Subsequent commands' bytes silently lost; persisted output appears truncated | `StreamReader.TruncateConsumed` does stop-pipe → truncate → restart-pipe; covered by `TestStreamReader_TruncateAtIdleBoundary` |
| A FIFO consumer disappearing during Go restart SIGPIPEs bash → tmux session dies → violates invariant #1 | Lost session + lost in-flight command on every Go restart | spec §3 chose regular-file + byte-offset over FIFO; covered by `TestStreamReader_ResumesFromPersistedOffset` and the E2E `TestE2E_GoRestart_DuringStreamingChunks` |
| bash on a PTY runs interactive: it emits readline bracketed-paste sequences (`\x1b[?2004h`/`l`) and a visible `bash-X.Y$ ` prompt *before* the wrapper's START sentinel, so those bytes land at the tail of the previous command's captured output | Stored output looks like real shell stdout *plus* mystery escape codes and partial prompts; users complain the UI doesn't match a terminal | After `stty -echo`, also send `bind 'set enable-bracketed-paste off'; PS1=''; PS2=''` — empty prompts have nothing to bleed. Asserted by `TestTmuxShell_Start_CreatesSessionAndConfigures`; verifiable end-to-end with `scripts/multi-session-smoke` |
| WSHandler snapshots `m.List()` at connect time and only subscribes to those sessions — new sessions created later via REST get no event path back to the open WS | Browser submits a `run` for the just-created session, server processes it, but `started`/`chunk`/`done` never reach the client. User sees a blank chat until they reload | `Manager.AddCreateListener` + a per-WS forwarder goroutine pushes the new session's events onto the existing FanIn output channel and writes an `idle` frame. Asserted by `scripts/new-session-ws` |
| `ansi-to-html` defaults `escapeXML: false` — it leaves angle brackets alone and only wraps SGR codes in `<span>` | A command output containing literal `<script>...</script>` injects DOM via `dangerouslySetInnerHTML` in `ChatStream` | `new AnsiToHtml({ escapeXML: true, ... })` in `ChatStream.tsx`; XSS guard asserted by `ChatStream.test.tsx` "HTML-escapes literal angle brackets" |
| `git commit` without `-m` launches `$EDITOR` (vi); we don't ship vi. bash hangs forever waiting for an editor that never appears, leaving `.git/COMMIT_EDITMSG` behind | UI shows a "running" command that will never end; Stop button is the only way out, and it SIGKILLs bash (resets cwd/env/aliases) | `validateGitCommit` in `internal/api/git_commit_guard.go` short-circuits at WS input validation; catches `-m foo`, `-m"foo"`, `-mfoo`, `--message foo`, `--message=foo`. Asserted by `git_commit_guard_test.go` and `TestE2E_GitCommit_RequiresDashM` |

---

## Layering / module boundaries

```
api/ (HTTP + WS — the only module that knows about all three below)
 ├── shell/   (bash + PTY + parser + broadcaster) — knows nothing about HTTP, users, storage
 ├── store/   (JSON file persistence)              — knows nothing about shell or users
 └── auth/    (credentials + rate limiter)         — knows nothing about shell or storage
static/      (embed.FS of web/dist with SPA fallback)
```

`shell` does not import `store`. The output buffer is handed to `api` via `EndedEvent.Output` and `api` calls `store.WriteOutput`. If you find yourself adding a `store.Store` parameter to `shell`, push back — keep the layering.

---

## What's intentionally NOT in the codebase

- No JWT / session store / refresh tokens.
- No user management / SSO / RBAC.
- No multi-session (per-user) tabs. The schema reserves room for a `session_id` field if you later need it.
- No PTY emulation. `vim` / `top` / `htop` will produce ANSI garbage; they're explicitly out of scope.
- No request-body / header logging. Commands may contain secrets in env vars; tokens are never logged.
- No automatic restart of interrupted commands at boot. The sweep marks them `interrupted` so the UI shows the truth; replay is unsafe (think `git push`).

---

## Provenance

- Brainstorm + spec: `docs/superpowers/specs/2026-06-11-headless-alfred-design.md`
- Implementation plans (the actual build journey, with code blocks for every step): `docs/superpowers/plans/2026-06-11-headless-alfred-*.md`
- Built over the course of 2026-06-11 via the superpowers brainstorm → spec → plan → implement loop. Real bugs discovered during integration and E2E are catalogued in the README's commit log.

---

## Quick orientation for "I want to change X"

| Change | Where to look first |
|---|---|
| The shell behavior (Stop, restart, buffer cap) | `internal/shell/shell.go` |
| How commands are framed in/out of bash | `internal/shell/sentinel.go` (and the `Wrap` function) |
| The HTTP API surface | `internal/api/{router,login,commands}.go` |
| WS protocol | `internal/api/ws.go` (and `web/src/lib/ws.ts` for the client) |
| Frontend UI (chat stream) | `web/src/features/terminal/ChatStream.tsx` (UserBubble + AssistantBlock pairs); `web/src/features/sessions/useSessions.ts` owns the hook; per-session state mutations live in `sessionsReducer.ts` |
| What's stored to disk | `internal/store/store.go` + `internal/store/record.go` |
| Container build | `Dockerfile` + `scripts/build-image.sh` |
| K8s manifests | `deploy/manifests/` |
| Cluster prerequisites + ops runbook | `deploy/README.md` |
| Add a new tmux operation | `internal/shell/tmuxio/runner.go` (also add to `FakeRunner`) |
| Modify session lifecycle (create, close, reconcile) | `internal/session/manager.go`; lifecycle listeners (`onCreate` / `onClose` / `onRename`) use the generic `listenerSet[T]` in `internal/session/listeners.go` |
| Add a new server-pushed WS frame type | `internal/api/wsproto.go` (add field to `OutMsg`) + `internal/api/ws.go` (emit it) + `web/src/lib/ws.ts` (add to `ServerMsg` union) + `web/src/features/sessions/sessionsReducer.ts` (handle it) |
| Change bash init env (echo, prompt, readline opts) | `internal/shell/tmux_shell.go` — `configurePane()` is the single source of truth, called from both `Start` and `Stop`'s respawn path |
| Change the sidebar UI | `web/src/features/sessions/SessionsSidebar.tsx` |
| Git auth UI / credential storage | `web/src/features/sessions/GitCredentialsDialog.tsx` (form) + `web/src/lib/api.ts:saveGitCredentials` (client) + `internal/api/git_credentials.go` (writes `~/.git-credentials` + `~/.gitconfig` `credential.helper=store`) |
| ANSI color rendering in chat output | `web/src/features/terminal/ChatStream.tsx` — `renderAnsi()` via `ansi-to-html` with `escapeXML:true`. Add new SGR codes by extending the `colors:` palette in the `AnsiToHtml` constructor |
| Validate/reject a command shape before it reaches bash | `internal/api/git_commit_guard.go` is the template — wired into the `case "run"` arm of `handleInbound` in `internal/api/ws.go`. Useful for any command that would hang because the container doesn't ship an interactive program |

When in doubt: read the regression test for the area before reading the production code. Tests document the bugs the code is shaped against.
