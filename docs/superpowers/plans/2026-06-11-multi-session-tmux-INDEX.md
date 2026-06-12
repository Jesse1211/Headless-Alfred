# Multi-session via tmux — Plan Index

Spec: `docs/superpowers/specs/2026-06-11-multi-session-tmux-design.md`

18 small plans. Each one is independently shippable (its own tests
stay green on its own scope). Later plans depend on earlier ones being
merged. The E2E plans (11-17) are one scenario per plan so reviews
stay small.

| # | Plan | Scope | Verified by |
|---|---|---|---|
| 1 | [Store schema](2026-06-11-multi-session-tmux-01-store-schema.md) | per-session directory layout, `SessionMeta`, `MigrateLegacyLayout` | `go test ./internal/store/` |
| 2 | [TmuxRunner + offset reader](2026-06-11-multi-session-tmux-02-tmux-runner.md) | TmuxRunner interface, FakeRunner, StreamReader | `go test ./internal/shell/tmuxio/` |
| 3 | [TmuxShell](2026-06-11-multi-session-tmux-03-tmux-shell.md) | real tmux-backed Shell event surface | `go test ./internal/shell/` (+ tag integration) |
| 4 | [session.Manager](2026-06-11-multi-session-tmux-04-manager.md) | CRUD, limit, reconcile, `exit` auto-close, Stop respawn | `go test ./internal/session/` |
| 5 | [REST](2026-06-11-multi-session-tmux-05-rest.md) | `/api/sessions*` + moved `/api/sessions/{sid}/commands*` | `go test ./internal/api/` |
| 6 | [WS protocol](2026-06-11-multi-session-tmux-06-ws.md) | sessionID on every msg + close/rename broadcasts + fan-in | `go test ./internal/api -run TestWS` |
| 7 | [Boot wiring](2026-06-11-multi-session-tmux-07-boot.md) | migrate → reconcile → listener order | `go build ./... && go test ./...` |
| 8 | [useSessions hook](2026-06-11-multi-session-tmux-08-use-sessions.md) | replaces useShell; per-session state Map | `npm test` |
| 9 | [SessionsSidebar](2026-06-11-multi-session-tmux-09-sidebar.md) | left sidebar, hover-×, double-click rename | `npm test` |
| 10 | [Frontend integration](2026-06-11-multi-session-tmux-10-frontend-integration.md) | WorkspacePage, ConfirmDialog, lazy history | `npm test && npm run build` |
| 11 | [E2E batch A](2026-06-11-multi-session-tmux-11-e2e-a.md) | 3 must-pass scenarios bundled (fs share + Go-restart + chunks). **Status: 2/3 PASS**; DuringStreamingChunks completes but pre-restart chunks are lost — see plan doc "Remaining gap". | `make e2e -run …` |
| 12 | [E2E Reconcile_StoredButNotLive](2026-06-11-multi-session-tmux-12-e2e-reconcile.md) | pod-restart branch | `make e2e -run …` |
| 13 | [E2E PtyStream_Truncation](2026-06-11-multi-session-tmux-13-e2e-truncation.md) | racy truncation no-bytes-lost | `make e2e -run …` |
| 14 | [E2E CrossSession_NoOutputBleed](2026-06-11-multi-session-tmux-14-e2e-no-bleed.md) | sentinel routing isolation | `make e2e -run …` |
| 15 | [E2E EightConcurrentSleeps](2026-06-11-multi-session-tmux-15-e2e-concurrency.md) | concurrency regression | `make e2e -run …` |
| 16 | [E2E CloseSession_RunningCommand](2026-06-11-multi-session-tmux-16-e2e-close.md) | delete kills in-flight + marks interrupted | `make e2e -run …` |
| 17 | [E2E SessionLimit + delete old](2026-06-11-multi-session-tmux-17-e2e-limit.md) | 9th POST → 422; drop 4 superseded tests | `make e2e -run …` |
| 18 | [Docs + recommended E2E + CI](2026-06-11-multi-session-tmux-18-docs.md) | 5 recommended E2E, README/CONTEXT/Dockerfile/CI | `make e2e && green CI` |

## Dependency graph

```
1 ─┐
2 ─┤         ┌─► 8 ─► 9 ─► 10 ─┐
   ├─► 3 ─► 4 ─► 5 ─► 6 ─► 7 ──┴─► 11 ─► 12..17 (any order) ──► 18
```

- **Plan 1 and Plan 2 are independent**
- **Plans 8/9/10 (frontend)** wait for Plan 6 (WS shape)
- **Plans 11-17 (E2E)** wait for Plan 7 (boot fully working)
- **Plan 18 (docs + CI)** is last
