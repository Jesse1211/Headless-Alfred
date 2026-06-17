# Releases

User-facing changelog. Process and branch model live in `RELEASE.md`
at the repo root.

Most-recent first.

---

## v0.1 — 2026-06-17

First tagged release. Marks "what's deployed on oracle k3s as of
today" as the baseline. Everything below was developed and shipped
incrementally over the preceding weeks; we're just drawing the
line here.

### Core platform

- **Multi-session bash + tmux**: up to 8 concurrent chat sessions,
  each backed by its own tmux session. Commands survive
  `alfred-server` restarts (tmux outlives Go process).
- **Chat-stream UI** for shell sessions: pretty-rendered user
  prompts + command output bubbles with ANSI color and live
  streaming.
- **Per-session naming, rename, close** from the left sidebar.

### Claude integration

- **Claude UI mode**: ChatGPT-style rendered chat (`react-markdown` +
  GFM + Prism syntax highlighting) that talks to `claude -p` per
  prompt.
- **Claude TUI mode**: xterm.js terminal for the interactive Claude
  CLI experience.
- **PreToolUse approval cards**: every tool call surfaces an Allow /
  Deny card in the chat. `AskUserQuestion` renders as a custom
  radio-select dialog and rides the answer back through the deny
  reason channel.
- **`--dangerously-skip-permissions` bypass mode**: per-session
  opt-in via the Start Claude dialog. Hook respects the choice and
  auto-allows tools (AskUserQuestion still asks).
- **Claude session cwd follows the shell pane**: cd into a dir in
  the shell, then enter Claude → Claude starts there.
- **Per-session summary template** (`summary-todo`): Claude
  maintains a short markdown summary in `/data/summaries/<sid>.md`
  on every turn.
- **Daily Recap** (singleton session): `recap-daily` template
  generates one markdown file per day in `/data/recaps/`. Right
  rail flips to RecapSidebar with date list when the recap session
  is selected.
- **Claude conversation history restore**: backend parses Claude
  CLI's own `~/.claude/projects/<...>/<uuid>.jsonl` to rebuild the
  chat after a page reload, including text/tool interleave order.
- **`expandedPrompt` toggle**: see exactly what was sent to Claude
  when the server injected a template body.
- **Right-rail "thinking" cards** (plumbing only — model emits when
  `alwaysThinkingEnabled` is true; not currently set).

### Right rail

- **Task Summary + Notes accordion** in a collapsible RightRail.
- **Notes panel** with debounced autosave; user-only scratchpad,
  never sent to Claude.
- **Path strip + Copy** on both Summary and Notes so you can grab
  the on-disk path with one click.

### UI polish

- **Session indicator dot** (one per session, in header and each
  sidebar row): green = idle, red = busy, yellow pulsing = needs
  decision (allow/deny or question), warning triangle = WS
  disconnected.
- **Resizable left and right sidebars** with collapse-to-rail.
- **Auth in left footer**: Git credentials, Claude credentials,
  Sign out.
- **IME-aware Enter**: composers don't ship half-finished IME
  commits.
- **Instant CSS tooltips** on header controls (zero-delay,
  data-tooltip attr).

### Deploy

- **Helm chart** for oracle k3s (single replica, Recreate
  strategy, Traefik ingress).
- **GitHub Actions deploy-on-push**: rsync → nerdctl build on
  oracle → helm upgrade → smoke /healthz → auto-rollback on
  failure.
- **Persistent PVC** covering both `/data` (Alfred state) and
  `/home/alfred` (git creds, Anthropic creds, Claude jsonl
  history, npm-installed Claude CLI). Pod restarts and deploys do
  not lose any of this.
- **Init container** chowns subPath mounts to UID 1000 on first
  deploy (fsGroup doesn't propagate to subPath).
- **Dev-mode dual-frontend**: `VITE_BACKEND_URL=http://alfred.local:8888
  npm run dev` gives Vite HMR on the frontend while talking to
  the deployed backend.

### Access pattern

Private only — no public DNS, no TLS. Reached from any client via
SSH tunnel + `/etc/hosts` mapping `alfred.local` → `127.0.0.1`.
See `deploy/README.md`.
