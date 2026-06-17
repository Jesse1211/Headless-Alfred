# Releases

User-facing changelog. Process and branch model live in `RELEASE.md`
at the repo root.

Most-recent first.

---

## v0.3 — 2026-06-17

UX polish + one v0.2 fix.

### Exit Claude is now mode-aware

UI mode (the ChatGPT-style renderer) used to SIGKILL the pane's
bash on Exit Claude, which reset the shell's cwd / env / aliases —
even though there's NO claude process to kill in UI mode between
prompts (it's per-prompt forked). Now Exit in UI mode just flips
the mode flag; bash stays put. Button reads **"Pause Claude"** with
a tooltip explaining the conversation resumes on next click (it
does — `--resume <uuid>` reads the jsonl on the PVC).

TUI mode keeps the SIGKILL+respawn path (a long-lived `claude` TUI
owns the pane and has to be killed). Button still reads "Exit
Claude" with a tooltip warning about the cwd/env reset.

### Disconnected indicator now shows why

Hovering the ⚠ disconnected dot used to show just "Disconnected".
Now it expands to a multi-line tooltip with:

  Disconnected
  code: 1006 (abnormal)
  last seen 12s ago
  reconnect attempt 3

Captures WebSocket close code + reason, last successful open, and
current retry count.

### TodoWrite progress card

Claude's TodoWrite tool calls used to render as folded JSON tool
cards. Now they get a real checklist card with status markers and
the current turn's elapsed time + cumulative token usage:

  Tasks (2/4)             12m 34s · 8,510 in → 1,230 out
    ✔ Stage 3b.1 — geometry.py
    ✔ Stage 3b.2 — cartoon.py
    ◼ Updating repository functions for cartoon
    ◻ Stage 3b.4 — render routes

### Per-prompt template multi-select

Templates moved from "always-on per session" to "checkbox under the
composer textarea, choose per-prompt". Sticky per session via
localStorage so your preferred default persists across reloads.
Backend now accepts a `templates[]` array on `claude_prompt`;
multiple selected templates concatenate in order, each preceded by
the `\\n\\n---\\n` separator. `GET /api/templates` lists available
templates (id + name).

### Fix: PVC monitoring now reads actual quota usage (was broken in v0.2)

The disk-pressure banner in v0.2 read `statfs(/data)`, which sees
the underlying node disk (~100GB on oracle), not the 5Gi PVC quota.
Banner read "19% used" while real PVC could be at 95%. Replaced
with a quota-aware computation: walk /data + ~/ for actual usage,
compare against `ALFRED_PVC_LIMIT` (Helm passes
`.Values.persistence.size`). Banner copy updated to "PVC X% full —
A used of B" instead of confusing "Disk X% full".

---

## v0.2 — 2026-06-17

Two small infra/UX features for self-managed deploys.

### PVC capacity monitoring

A thin banner appears at the top of the workspace when the
backing PVC is filling up:

- ≥ 80% used → yellow warning banner
- ≥ 95% used → red banner with subtle pulse
- Below 80% → hidden (no noise when fine)

Backend probes `/data` via statfs every 60s and pushes a
`disk_usage` WS frame to all clients only when the alert level
flips (or on first connect). Read-only fetch endpoint:
`GET /api/disk-usage` returns `{path, totalBytes, usedBytes,
availableBytes, usedPercent}` for ad-hoc debugging.

`/data` and `/home/alfred` share one PVC, so the umbrella
percentage is what matters — that's what we monitor and surface.

### Claude CLI runtime upgrade

New "Claude version" button in the left sidebar footer opens a
dialog showing the currently-installed CLI version with an input
to upgrade to `latest` / `next` / a specific `X.Y.Z`. The npm
output streams live into the dialog (no spinning-then-done UX).

Updates land in `~/.npm-global/` on the PVC (entrypoint set npm
prefix there in v0.1), so they survive pod restarts. The next
Claude UI prompt automatically forks the new binary via PATH
lookup — no service restart, no redeploy.

Server-side: strict version-string regex (`latest|next|X.Y.Z`)
and hardcoded package name prevent the endpoint from being
coerced into installing arbitrary npm packages.

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
