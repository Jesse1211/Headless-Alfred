# Headless Alfred — Implementation Plan Index

Design spec: [`../specs/2026-06-11-headless-alfred-design.md`](../specs/2026-06-11-headless-alfred-design.md)

Each plan below is independently executable and produces a verifiable artifact. Execute them in order.

| # | Plan | Produces | Verification |
|---|---|---|---|
| 1 | [Backend core](2026-06-11-headless-alfred-01-backend-core.md) | `internal/shell`, `internal/store`, `internal/auth` Go packages | `go test ./internal/...` green |
| 2 | [Backend API & wiring](2026-06-11-headless-alfred-02-backend-api.md) | `internal/api`, `internal/static`, `cmd/alfred-server` | `curl` against local binary works end-to-end |
| 3 | [Frontend](2026-06-11-headless-alfred-03-frontend.md) | React app under `web/` | `npm run dev` shows working UI against local backend |
| 4 | [Container & K8s deploy](2026-06-11-headless-alfred-04-deploy.md) | `Dockerfile`, `deploy/manifests/*.yaml` | Image builds; deploy works on any k8s cluster |
| 5 | [E2E in kind](2026-06-11-headless-alfred-05-e2e.md) | `test/e2e/` with kind setup + 7 test scenarios | `make e2e` passes locally and in CI |

## Implementation Order Rationale

- Plan 1 has no dependencies; pure Go modules with clear interfaces.
- Plan 2 wires them together behind HTTP/WS. After this, the backend is fully usable (curl, websocat).
- Plan 3 builds the UI against the working backend. Can run frontend dev server pointed at backend dev server.
- Plan 4 packages and deploys what's been built. Cluster prerequisites (cert-manager, DNS) verified by hand.
- Plan 5 closes the loop with automated end-to-end tests.

Skip ahead at your own risk — Plan 3 can begin in parallel with Plan 2 once API surface is locked (it is, see spec §6), but downstream plans assume upstream artifacts exist.
