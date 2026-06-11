# Headless Alfred — Context

This document is for someone (human or LLM) about to work on this codebase. It captures the **why** and the **non-obvious**, not the **what** — the code already tells you the latter.

If you have less than 5 minutes, read just **Mental model** and **Three invariants that don't bend**.

---

## Mental model

One Go process per Pod. That process owns:

1. **One bash subprocess** spawned at startup with `--noprofile --norc` and empty `PS1`. It lives until the Go process dies.
2. **One PTY** that bash's stdin/stdout flow through. Output is parsed via sentinel markers (`\x1eALFRED_START_<nonce> <cmdID> <cwd>\x1e`) wrapped around each user command.
3. **Two broadcasters** (raw bytes + typed events). Subscribers come and go (WebSocket connections, the disk-writer). The bash process never blocks on a slow subscriber.
4. **A JSON file store** at `/data` — one file per command for metadata, one for output. Atomic writes via tmp+rename.
5. **An HTTP+WS surface** for the React client.

The React app is just a viewer. **All state of consequence lives in the Go process and on the PVC.** Close the browser → bash keeps running → reconnect → reattach event delivers the in-memory buffer so far.

---

## Three invariants that don't bend

If you find yourself violating any of these, stop and think.

### 1. Bash lifecycle ≠ WebSocket lifecycle.

Bash is born when Go starts and dies when Go stops. The set of WebSocket connections is orthogonal. If you add code that ties a bash command to a specific connection, you have introduced a regression that breaks the entire premise of the project (long-running jobs surviving a closed laptop).

### 2. Output buffer lives where commands live: in `shell.Shell`.

The `EndedEvent.Output` field carries the buffer at the moment of completion. **Do not** try to read the buffer via `Shell.CurrentCommand()` after a command ends — `currentCmd` is already nil by then. This bit us during Plan 2; the fix was to attach the buffer to the event itself.

### 3. `waitLoop` must atomically mark the shell unavailable.

When bash dies, `waitLoop` must clear `currentCmd`, `available`, `cmd`, `pty` **under the same lock** before releasing it to publish the Ended event. Otherwise a concurrent `Write()` between unlock and the restart's re-lock can sneak in, write to a closed PTY, and brick the shell with an orphan command that no event will ever clear. There's a comment in `internal/shell/shell.go` `waitLoop` explaining this; respect it.

---

## Why these choices

| Choice | Why |
|---|---|
| Single user, hardcoded credentials | Personal tool. Per spec §8: K8s Secret env vars, static long-random token, no JWT/session. Acceptable trade-off given Secret rotation = pod restart. |
| One bash, one command at a time | Spec §3. Concurrency is what `&` is for. Multi-tab/session was explicitly deferred (spec §14). |
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
| Frontend UI | `web/src/features/terminal/` |
| What's stored to disk | `internal/store/store.go` + `internal/store/record.go` |
| Container build | `Dockerfile` + `scripts/build-image.sh` |
| K8s manifests | `deploy/manifests/` |
| Cluster prerequisites + ops runbook | `deploy/README.md` |

When in doubt: read the regression test for the area before reading the production code. Tests document the bugs the code is shaped against.
