# Multi-session via tmux — Plan Index

Spec: `docs/superpowers/specs/2026-06-11-multi-session-tmux-design.md`

14 small plans, each independently shippable (its own tests stay green
on its own scope). Later plans depend on earlier ones being merged.

| # | Plan | Scope | Verified by |
|---|---|---|---|
| 1 | [Store schema](2026-06-11-multi-session-tmux-01-store-schema.md) | per-session directory layout, `SessionMeta`, `MigrateLegacyLayout` | `go test ./internal/store/` |
| 2 | [TmuxRunner abstraction + offset stream reader](2026-06-11-multi-session-tmux-02-tmux-runner.md) | mockable tmux interface, `pty.stream` reader with offset + sentinel-aligned truncate | `go test ./internal/shell/` (no real tmux) |
| 3 | [TmuxShell replaces Shell](2026-06-11-multi-session-tmux-03-tmux-shell.md) | real tmux-backed implementation of the `Shell` event interface | integration test with real tmux + bash |
| 4 | [session.Manager + reconciliation](2026-06-11-multi-session-tmux-04-manager.md) | CRUD, 8-limit, startup reconcile, `exit` auto-close, Stop respawn | `go test ./internal/session/` |
| 5 | [REST API `/api/sessions*`](2026-06-11-multi-session-tmux-05-rest.md) | 5 new endpoints; old `/api/commands*` moved under `/api/sessions/{id}/...` | `go test ./internal/api/` + curl walkthrough |
| 6 | [WS protocol with sessionID](2026-06-11-multi-session-tmux-06-ws.md) | every WS message carries sessionID; `session_closed`/`session_renamed` broadcast; multi-session reattach | `go test ./internal/api -run WS` |
| 7 | [Boot wiring: migrate + reconcile + listener order](2026-06-11-multi-session-tmux-07-boot.md) | `main.go` runs migration → reconcile → only then opens HTTP listener | real `alfred-server` boot with fixture data |
| 8 | [Frontend `useSessions` hook](2026-06-11-multi-session-tmux-08-use-sessions.md) | replaces `useShell`; per-session state map; localStorage selectedID; cross-tab close handling | `npm test` |
| 9 | [Frontend Sidebar component](2026-06-11-multi-session-tmux-09-sidebar.md) | "+ New chat" button (disabled at 8), session rows with hover-×, double-click rename inline input | `npm test` + Storybook-style snapshot |
| 10 | [Frontend integration: empty state, confirm dialogs, switch](2026-06-11-multi-session-tmux-10-frontend-integration.md) | wire Sidebar to `useSessions`, render empty state, deletion confirm dialog, discard draft on switch | Playwright screenshot regressions |
| 11 | [E2E batch A: filesystem share, Go-restart survives, streaming chunks](2026-06-11-multi-session-tmux-11-e2e-a.md) | 3 must-pass E2E from §9 | `make e2e` (3 new scenarios) |
| 12 | [E2E batch B: reconcile-stored-not-live, pty.stream truncation, cross-session no bleed](2026-06-11-multi-session-tmux-12-e2e-b.md) | 3 must-pass E2E (the raciest ones) | `make e2e` |
| 13 | [E2E batch C: 8 concurrent sleeps, Close kills in-flight, session limit](2026-06-11-multi-session-tmux-13-e2e-c.md) | 3 remaining must-pass E2E + delete 4 superseded old tests | `make e2e` |
| 14 | [Recommended E2E + docs + CI](2026-06-11-multi-session-tmux-14-recommended-and-docs.md) | 5 recommended E2E; update README + CONTEXT.md + CI workflow; tag a release | `make e2e` all-green + green CI |

## Dependency graph

```
1 ─┐
2 ─┤         ┌─► 8 ─► 9 ─► 10 ─┐
   ├─► 3 ─► 4 ─► 5 ─► 6 ─► 7 ──┴─► 11 ─► 12 ─► 13 ─► 14
```

- **Plan 1 and Plan 2 are independent** — can be done in parallel by
  two engineers or two subagents.
- **Plans 8/9/10 (frontend)** wait for Plan 6 (WS protocol shape).
- **Plans 11-14 (E2E)** wait for Plan 7 (boot fully working).

## Recommended ordering for a solo engineer

1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14.

Plan 1 + 2 can be done in any order (or even in parallel branches and
merged). Everything else is strictly sequential.
