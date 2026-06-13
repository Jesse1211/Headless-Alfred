# Headless Alfred

A web-based control panel for up to 8 concurrent persistent bash sessions living on your cloud server.
Type a command in the browser → it runs on the server → output streams back live.
Close the laptop, the session keeps running. `kubectl rollout` the server, sessions keep running.
Come back, pick up where you left off.

Domain: **agent.jesseliu.me** (production). Single user. Personal tool.

```
┌──────────────────┬──────────────────────────────────────┐
│ + New chat       │ training                  ● Sign out │
├──────────────────┼──────────────────────────────────────┤
│ ACTIVE SESSIONS  │                                [ ls ]│
│  • Session 1     │   CONTEXT.md Makefile deploy go.mod  │
│  • training ←sel │   exit 0                             │
│  • db-debug      │ ──────────────────────────────────── │
│                  │                              [ pwd ] │
│                  │   /Users/jesseliu/Desktop/...        │
│                  │   exit 0                             │
│                  ├──────────────────────────────────────┤
│                  │ ( Type a command…                ↑ ) │
└──────────────────┴──────────────────────────────────────┘
```

Up to 8 concurrent bash sessions, each independent (own cwd / env / aliases) but sharing the container's filesystem. `mkdir foo` in one session is visible from another. Sessions survive Go-process restarts (e.g. `kubectl rollout`); Pod restarts reset them but keep the per-command history on the PVC.

ChatGPT-style chat stream: your command on the right, output on the
left, a thin separator between each turn. Long history scrolls the whole
page. Close the tab any time — commands keep running on the server.

---

## Using git

The container ships with `git` + `openssh-client`. The four commands the
chat stream officially supports are `git clone`, `git pull`, `git commit -m "msg"`,
and `git push` — others (`status`, `log`, `diff`, `checkout`, …) work fine
too, but are not "officially supported" only in the sense that we don't
go out of our way to test them.

**Authentication.** Click the **⚙ gear** in the header → "Git
credentials" → paste your host (`github.com`), username, and a personal
access token (PAT). The server stores them in `~/.git-credentials` (mode
0600, persists on the PVC) and configures git's `credential.helper=store`,
so subsequent clones/pulls/pushes authenticate silently. The token never
appears in the chat stream or in command history. Re-submit the dialog
to rotate.

**Caveats inherited from running git inside a chat UI:**

- `git commit` **must** include `-m "msg"`. Without it, git tries to
  launch `$EDITOR` which we don't ship; the server catches this and
  shows an inline error before bash hangs.
- `git pull` may leave a merge-message editor dialog open if the merge
  isn't fast-forward. Use `git pull --no-edit` or `git pull --rebase`.
- Progress bars from `git clone` / `git push` don't redraw in-place
  the way a real terminal does — they show every intermediate line.
- `Ctrl+C` doesn't reach git. The Stop button SIGKILLs bash and respawns
  it; cwd resets to `/home/alfred`.
- ANSI colors (`git status`, `git log --color=always`) **do** render —
  if a command's output looks dimmer than your terminal, set
  `git config --global color.ui always`.

For repos larger than the PVC (default 1 GB), expand `pvc.yaml` before
cloning, or `git clone --depth 1` to save space.

---

## Using Claude

The container also ships `node` + `@anthropic-ai/claude-code`. Inside
any shell session, type either **`claude`** (the actual CLI, like
in a real terminal) or **`/claude`** (a shortcut) in the command
input to flip that session into Claude mode. The chat-stream view
is replaced with a real terminal (xterm.js), and `claude` is spawned
**in the same tmux pane** so it inherits the current cwd, env, and
any files you just `git clone`d.

To exit, click the red **"Exit Claude"** button in the header. We
send Ctrl+C twice — claude may need another keypress or `/exit` to
finish cleanly. The button immediately flips the UI back to the
chat view; if claude is stubborn, you can re-enter the session to
finish it off.

**Authentication.** `claude` inside the pod can authenticate two ways.
Pick whichever matches what you're paying for.

*Option A — OAuth credentials from another machine (uses your Claude
Pro / Max / Team subscription).* On a machine where you've already
run `claude /login` successfully (your laptop, etc.):

```bash
# macOS — the token lives in Keychain, extract it as JSON:
security find-generic-password -s "Claude Code-credentials" -w
# Linux — the token is already on disk:
cat ~/.claude/.credentials.json
```

Copy the entire JSON object. In the browser, click the **⚙ gear** →
**Claude credentials**, paste the JSON, Save. The server installs it
at `~/.claude/.credentials.json` (mode 0600) on the PVC, so it
survives Pod restarts. claude reads from this file on every launch;
refresh-token rotation is handled by claude itself.

*Option B — API key (uses pre-paid API credits, billed separately
from a Claude.ai subscription).* Get a key from
https://console.anthropic.com/settings/keys, then in a session:

```bash
mkdir -p ~/.claude
echo "ANTHROPIC_API_KEY=sk-ant-..." > ~/.alfred-env
chmod 600 ~/.alfred-env
```

Then inside any session before running `claude`, do `source ~/.alfred-env`.
(We don't source `.bashrc` automatically — see CONTEXT.md.) Option A
is the recommended path because it reuses an existing subscription;
Option B exists as a fallback for users who specifically want
metered API access.

**Verified end-to-end** with a real OAuth credential from a Mac
Keychain pasted through the dialog — claude in the pod authenticates
and proceeds straight to the main UI without a `/login` prompt.

**Caveats:**
- Mode is **persisted** to `sessions.json`. Restart `alfred-server`
  (e.g. `kubectl rollout`) and your Claude session resumes; restart
  the Pod and tmux dies, so does Claude — mode is reset to shell on
  Reconcile.
- **Exit Claude resets the session's bash** (cwd, env vars, aliases
  go back to defaults — same trade-off as the Stop button in shell
  mode). The PVC is unchanged, so any files Claude created stay; you
  just need to `cd` back to where you were.
- A session can run **either** a normal shell command **or** Claude
  at a time, never both. The `Claude` button is disabled while a
  shell command is busy.
- xterm.js renders ANSI colors / box-drawing correctly. Resize-aware
  PTY isn't wired up yet — Claude sees a fixed 80x24 and wraps.

---

## Usage flow

### 0. One-time setup of the cloud server

You need a k3s cluster reachable at `agent.jesseliu.me`. See [`deploy/README.md`](deploy/README.md) for the full runbook. Summary:

- k3s installed (Traefik enabled by default)
- cert-manager installed with a `letsencrypt-prod` ClusterIssuer
- DNS A record `agent.jesseliu.me` → cluster public IP
- (Optional) GHCR pull secret if the image is private

### 1. First-time deploy

```bash
# Create namespace + secret (the one thing not in git).
kubectl apply -f deploy/manifests/namespace.yaml
kubectl -n alfred create secret generic alfred-secret \
  --from-literal=ALFRED_USER=admin \
  --from-literal=ALFRED_PASSWORD='<your strong password>' \
  --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)

# Build + push the image to your registry, then apply manifests.
make -C deploy push apply
```

Wait until the certificate is Ready (`make -C deploy status`), then open https://agent.jesseliu.me in a browser.

### 2. Day-to-day from the browser

1. Open https://agent.jesseliu.me — login page appears.
2. Sign in with `ALFRED_USER` / `ALFRED_PASSWORD`. Token persists in localStorage, so subsequent visits skip login until you sign out.
3. The terminal page shows:
   - **Left**: command history (most recent on top).
   - **Right**: output of the running or selected command.
   - **Bottom**: command input. Enter to run, Shift+Enter for newline.
4. Type any command. While it runs:
   - Output streams live.
   - History is locked (one command at a time).
   - A red **Stop** button replaces Run; click to SIGKILL bash (loses cwd, but reliably interrupts long jobs).
5. Close the tab any time. The command keeps running on the server. Reopen the tab and click in — the WS reattaches and resumes streaming.

### 3. Updating the running app

After a code change:

```bash
git pull
make -C deploy push          # build + push :SHORT_SHA
make -C deploy set-image     # rolling update to the new image
```

State preserved: command history JSON files on the PVC survive. State lost: in-flight bash session (the cwd, env vars, aliases). The user re-cds.

### 4. Rotating credentials

```bash
kubectl -n alfred delete secret alfred-secret
kubectl -n alfred create secret generic alfred-secret \
  --from-literal=ALFRED_USER=admin \
  --from-literal=ALFRED_PASSWORD='<new>' \
  --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)
make -C deploy rollout-restart
```

All issued tokens become invalid; users must log in again.

---

## Local development

### Run both backend and frontend dev servers

```bash
# Terminal 1 — backend with throwaway env, on :8080
ALFRED_USER=admin ALFRED_PASSWORD=test ALFRED_TOKEN=devtoken \
  ALFRED_DATA_DIR=/tmp/alfred-dev go run ./cmd/alfred-server

# Terminal 2 — Vite dev server on :5173 (proxies /api and /ws to :8080)
cd web && npm run dev
```

Open http://localhost:5173. Login `admin` / `test`. Vite HMR makes frontend iteration fast.

### Run the production binary locally (no Vite, single binary serving SPA + API + WS)

```bash
make embed-web build           # build frontend → copy to internal/static/dist → build Go binary
ALFRED_USER=admin ALFRED_PASSWORD=test ALFRED_TOKEN=devtoken \
  ALFRED_DATA_DIR=/tmp/alfred-dev ./bin/alfred-server
```

Open http://localhost:8080.

### Automated tests

```bash
make test            # Go: 65 unit + integration tests (race detector on)
cd web && npm test   # 9 vitest hook tests
make smoke           # binary smoke (HTTP-only, no WS)
make ws-smoke        # full headless end-to-end against the local binary
```

### Full end-to-end in a real Kubernetes cluster (kind)

```bash
make e2e-setup       # ~90s: build image, create kind cluster, deploy, port-forward
make e2e             # 7 spec scenarios against the live cluster
make e2e-teardown    # delete only the alfred-e2e cluster
```

### Is it running? Cleaning up

```bash
make local-status    # shows: alfred-server processes, port-forwards, listening ports, kind clusters
make local-stop      # kills any local alfred-server + kubectl port-forward, leaves kind cluster alone
```

A few notes that come up:

- **Vite picks the next free port.** If `:5173` is already in use by another project's dev server, Vite falls back to `:5174`, `:5175`, etc. — read the line `Vite ready in ... → Local: http://localhost:NNNN/` it prints on startup.
- **Multiple `ALFRED_DATA_DIR` mistakes are easy.** A second `go run` against the same data dir while the first is still running shares JSON files but each process owns its own bash — confusing. Either use different dirs (`/tmp/alfred-dev-1`, `/tmp/alfred-dev-2`) or `make local-stop` first.
- **kind cluster `alfred-e2e` persists across runs** until you `make e2e-teardown`. Useful: iterate fast on tests. Tradeoff: it eats a couple GB of RAM. The `local-stop` target does NOT delete it; use `make e2e-teardown` explicitly.
- **Port 8080 in `lsof` may be Docker Desktop**, not us. Docker Desktop's `com.docker.backend` listens on miscellaneous ports for internal use. Check `pgrep -fl alfred-server` to be sure.

---

## How it works

```
[Browser]
   │ HTTPS
   ▼
[Traefik in k3s] ── cert-manager → Let's Encrypt
   │
   ▼
[Service alfred → :8080]
   │
   ▼
[Pod — single replica]
   Go binary:
     • Static React build (embedded via go:embed)
     • HTTP API: /api/login, /api/sessions, /api/sessions/{sid}/commands/*, /healthz, /readyz
     • WebSocket: /ws (token in query string)
     • Up to 8 tmux sessions, each owning one bash via PTY (sentinel-wrapped commands)
     • tmux server outlives alfred-server (daemonized, reparented to tini)
     • Per-session directory; per-command JSON + .log
   │
   ▼
[PVC /data — RWO 1 GB]
   ├ sessions.json                    (atomic tmp+rename)
   ├ alfred-tmux.sock                 (tmux server socket)
   └ sessions/<sid>/
       ├ commands/<cmdID>.json
       ├ outputs/<cmdID>.log
       ├ pty.stream                   (tmux pipe-pane writes here)
       └ pty.offset                   (parser consumed-byte offset, for resume)
```

Key invariant: **bash lifecycle ≠ WebSocket lifecycle ≠ Go-process lifecycle**. Each session's bash lives in a tmux session that outlives both the WebSocket *and* the alfred-server process. Browsers disconnect, alfred-server restarts (`kubectl rollout`) — commands keep running. Only a Pod restart kills them. See [`docs/superpowers/specs/2026-06-11-multi-session-tmux-design.md`](docs/superpowers/specs/2026-06-11-multi-session-tmux-design.md) for the full design.

---

## Repo layout

```
cmd/alfred-server/      Go main entry point
internal/
  shell/                TmuxShell wrapping a tmux runner; sentinel parser; event broadcaster
    tmuxio/             tmux command runner (real + fake) + stream reader
  session/              Manager: lifecycle, listeners (listenerSet[T]); reconcile on boot
  store/                per-session disk layout, sessions.json (atomic tmp+rename)
  auth/                 static credentials + per-IP login rate limit
  api/                  HTTP/WS handlers, middleware, router; wsproto.go owns OutMsg/InMsg
  static/               go:embed of web/dist for production
web/                    React + Vite + TypeScript SPA
  src/lib/              fetch + WebSocket clients (ShellSocket reconnects with backoff)
  src/features/auth     login page + useAuth hook
  src/features/sessions useSessions hook + sessionsReducer (pure) + SessionsSidebar + ConfirmDialog + WorkspacePage
  src/features/terminal ChatStream + CommandInput (presentation only)
deploy/
  manifests/            k8s YAMLs for namespace, pvc, secret template, deployment, service, ingress
  Makefile              operator targets (push, apply, set-image, logs, status)
  README.md             cluster prerequisites + runbook
scripts/
  build-image.sh        wraps docker build with sane defaults
  smoke.sh              HTTP-layer binary check
  ws-smoke/             headless end-to-end (login + WS run + verify)
test/e2e/               kind-based 7-scenario E2E
docs/superpowers/
  specs/                design document(s)
  plans/                implementation plan documents (the build journey)
```

---

## License

MIT — see [LICENSE](LICENSE).
