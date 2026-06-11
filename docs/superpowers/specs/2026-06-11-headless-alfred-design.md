# Headless Alfred — Design

**Date**: 2026-06-11
**Status**: Approved, ready for plan
**Domain**: `agent.jesseliu.me`

## 1. Problem

A web app that lets the author run shell commands on his cloud server from a browser. The shell stays alive across browser disconnects, so long-running commands (training jobs, builds) keep going while the laptop is closed. On reconnect, the UI re-attaches to the running command and resumes streaming output.

Single user (the author). Personal tool. No multi-tenancy.

## 2. Non-goals

- Multiple concurrent shell sessions / multiple tabs (single bash, single command at a time)
- Full PTY terminal emulation (no `vim`, `top`, `htop`, no ANSI cursor control). Commands like `ls`, `cd`, `git`, `npm`, `python` work; interactive TUIs do not
- User management, SSO, RBAC, audit log
- File browser, code editor, embedded Claude Code controls
- Mobile responsive layout

## 3. Architecture

```
agent.jesseliu.me
   ↓ HTTPS
[Traefik Ingress in k3s, cert-manager → Let's Encrypt]
   ↓
[Service alfred-svc :8080]
   ↓
[Pod, single container]
   Go binary:
     - embedded React dist (SPA)
     - HTTP API + WebSocket
     - one persistent bash via PTY (creack/pty)
     - broadcaster fan-out of PTY output
     - JSON file store
   ↓
PVC mounted at /data
   ├─ /data/commands/<id>.json   (per-command metadata)
   └─ /data/outputs/<id>.log     (per-command output, cap 10 MB)
```

**Single container, single Go process, single bash.** Chosen for minimal moving parts. tmux deliberately omitted — bash and Go share container lifecycle anyway, so tmux adds layers without resilience gain.

## 4. Module boundaries

### Backend (`cmd/alfred-server` + `internal/`)

```
internal/
  shell/    Holds bash + PTY. Exposes Start, Write(cmd, id), Stop, Subscribe.
            Knows nothing about HTTP, users, storage.

  store/    Persists commands and outputs as JSON files. Exposes Save, List,
            Get, AppendOutput. Knows nothing about shell or users.

  auth/     Validates static credentials and tokens from env vars.
            Exposes CheckLogin(user, pass) -> token, VerifyToken(token).

  api/      HTTP handlers + WebSocket hub. The only module that knows about
            shell, store, and auth together.

  static/   `embed` of React dist, serves SPA with index.html fallback for
            client-side routes.
```

### Frontend (`web/src/`)

```
features/
  auth/
    LoginPage.tsx
    useAuth.ts          (token in localStorage, 401 interceptor)
  terminal/
    TerminalPage.tsx    (layout)
    HistoryList.tsx     (left sidebar, paged list)
    OutputView.tsx      (right pane, streams or shows historical)
    CommandInput.tsx    (bottom bar, locked when busy)
    useShell.ts         (WS hook: reattach / idle / chunk / done)
lib/
  api.ts                (fetch wrapper, attaches token, handles 401)
  ws.ts                 (WS wrapper, exponential reconnect, ping/pong)
App.tsx                 (route guard)
```

### Deploy (`deploy/`)

```
Dockerfile              (multi-stage: node build dist, go build binary)
manifests/
  namespace.yaml
  secret.example.yaml   (template; real Secret created out-of-band)
  deployment.yaml       (image, env from Secret, volumeMount /data)
  service.yaml          (ClusterIP :8080)
  ingress.yaml          (Traefik + cert-manager annotations)
  pvc.yaml              (1 GB)
```

## 5. Disconnect / reconnect invariants

**Core rule: bash lifecycle is independent of WebSocket lifecycle.**

- `shell.Start()` runs once at Go startup. bash dies only when Go dies (= container restart).
- `store` is a permanent subscriber to the shell broadcaster, written from goroutines internal to the shell module. Output is persisted regardless of WebSocket presence.
- Each WebSocket is a transient subscriber. It registers on connect, unregisters on close. Its presence/absence has zero effect on bash or storage.
- The broadcaster uses a ring buffer per subscriber (64 KB). A slow subscriber that falls behind gets a marker indicating dropped bytes; bash and other subscribers are never blocked.

### Reconnect protocol

On WebSocket open (after token check):

- If `shell.currentCmd != nil`, send `{type: "reattach", cmdId, command, startedAt, outputSoFar}` containing all output buffered so far for the running command, then begin streaming live `chunk` messages.
- Else send `{type: "idle"}`.

The frontend uses these two messages to set its initial state: input locked vs unlocked, right pane content, history highlight.

### Behavior matrix

| Event | Effect on bash | Effect on storage | Effect on UI on reconnect |
|---|---|---|---|
| Browser tab closed | none | none | history shows command as still running; reattach on next open |
| Laptop sleeps / network drops | none | none | same |
| Go process crashes | bash dies | last running command marked `interrupted` on next start (sweep on boot) | history shows `interrupted` badge |
| Pod restarted | bash dies | same as above | same |
| User clicks Stop | SIGINT sent to bash via PTY | command finishes with non-zero exit, marked `stopped` | input unlocks |
| User submits command while one is running | rejected | n/a | error toast `busy` |

Interrupted commands are **not** auto-restarted. They could have side effects (`git push`, file writes); replaying them is unsafe.

## 6. API surface

### HTTP

| Method | Path | Body / Query | Returns | Auth |
|---|---|---|---|---|
| `POST` | `/api/login` | `{user, password}` | `{token}` or 401 | none |
| `GET` | `/api/commands` | `?limit=100&before=<id>` | `[{id, command, status, started_at, finished_at, exit_code}]` (no output) | Bearer |
| `GET` | `/api/commands/:id` | — | full record including output | Bearer |
| `POST` | `/api/commands/:id/stop` | — | `204` or 409 if not running | Bearer |
| `GET` | `/ws` | `?token=<t>` | WebSocket upgrade | query token |
| `GET` | `/healthz` | — | `200 OK` | none |
| `GET` | `/readyz` | — | `200 OK` once shell + storage ready | none |
| `GET` | `/*` | — | static files; `index.html` for SPA routes | none |

### WebSocket messages

```ts
// client → server
{ type: "run", command: string }
{ type: "ping" }

// server → client
{ type: "reattach", cmdId, command, startedAt, outputSoFar }
{ type: "idle" }
{ type: "started", cmdId, command, startedAt }
{ type: "chunk", cmdId, data }                   // base64-encoded bytes
{ type: "done", cmdId, exitCode, finishedAt }
{ type: "error", code, message }                 // e.g. code="busy" | "auth_expired" | "shell_restarted"
{ type: "pong" }
```

## 7. Data formats

### `/data/commands/<id>.json`

```json
{
  "id": "01HAB...",                              // ULID
  "command": "npm run train",
  "cwd": "/workspace",
  "started_at": "2026-06-11T14:23:01Z",
  "finished_at": "2026-06-11T16:23:55Z",
  "exit_code": 0,
  "output_path": "/data/outputs/01HAB....log",
  "output_truncated": false,
  "status": "completed"
}
```

`status` values: `running` | `completed` | `interrupted` | `stopped`.

### `/data/outputs/<id>.log`

Raw byte stream, append-only. Capped at 10 MB; if the running buffer exceeds the cap, the head is dropped and `output_truncated` is set to `true` in the metadata.

### Atomicity

Metadata writes go through `write tmp → rename` so readers never see a partial JSON file. Output appends are direct (`os.O_APPEND`); a crash may leave a trailing partial line, which is acceptable.

### History indexing

MVP: `readdir(/data/commands)` sorted by mtime descending. Adequate up to several thousand commands. If/when this becomes slow, a separate `/data/index.jsonl` with metadata-only rows can be added later.

## 8. Authentication

Intentionally minimal.

- Three env vars (from K8s Secret): `ALFRED_USER`, `ALFRED_PASSWORD`, `ALFRED_TOKEN`.
- `ALFRED_TOKEN` is a static long random string (`openssl rand -hex 32`), generated once at deployment.
- `POST /api/login` checks `user == ALFRED_USER && password == ALFRED_PASSWORD`, returns `ALFRED_TOKEN` on success.
- Every authenticated HTTP request must carry `Authorization: Bearer <ALFRED_TOKEN>`.
- WebSocket upgrade carries `?token=<ALFRED_TOKEN>` in the query string; token is validated before the upgrade completes.
- No session storage, no JWT, no expiry, no refresh. Restart Go → same token still works (env unchanged).
- Rotation = update Secret + `kubectl rollout restart`.

Trade-off accepted: any process that can read pod env vars can impersonate the user forever until rotation. Mitigated by Secret being out of git and only owner accessing the cluster.

## 9. Error handling

| Category | Examples | Server behavior | UI behavior |
|---|---|---|---|
| Bad input | wrong password, expired/wrong token, empty command, `run` while busy | HTTP 401 / WS `{type:"error", code:"..."}` | red toast with clear message |
| Shell death | bash killed by OOM, kernel kill | mark running command `interrupted`, attempt one bash restart, broadcast `{type:"error", code:"shell_restarted"}` | banner "shell restarted, retry your command" |
| Storage IO | PVC full, fs error | log; do not change command status (command itself may still be running fine); keep streaming to WS subscribers from memory buffer | yellow banner "storage degraded, history may be incomplete" |
| Network / WS | client drops, idle timeout | unsubscribe transient subscriber; nothing else | exponential reconnect (1, 2, 4, 8, 16, 30 s cap), status dot in header |
| Panic | Go bug | `recover` middleware, log stack, return 500 / close WS with 1011 | "internal server error" |

### Invariants enforced in code

1. A `store` write failure must not propagate to `shell`. Storage degrades; bash keeps running.
2. Token validation runs before WebSocket upgrade completes.
3. `shell.Write` returns `ErrBusy` if `currentCmd != nil`. The API layer translates this to `{type:"error", code:"busy"}`.
4. Metadata writes are atomic (`tmp + rename`). Output writes use `O_APPEND`.
5. Every WebSocket message except `ping`/`pong`/`error` carries a `cmdId`.

### Logging

`log/slog` to stdout. Structured fields: `cmdId`, `user`, `remoteAddr`, `err`. Never log `password`, `token`, or full command text.

## 10. Security checklist

- [x] HTTPS enforced at Traefik (`redirectScheme: https`)
- [x] Token validation before WebSocket upgrade
- [x] Command length capped at 4 KB
- [x] Rate limit on `/api/login`: 5 attempts / minute per IP (in-memory token bucket)
- [x] No logging of password, token, or command content
- [x] PVC mounted with mode `0700`
- [x] No `pprof`, no debug endpoints in production binary
- [x] Same-origin only; CORS disabled
- [x] `imagePullPolicy: IfNotPresent` to avoid surprise upstream pulls

## 11. UI behavior

Split-screen layout (chosen during brainstorming):

```
┌──────────┬──────────────────────────────────┐
│ History  │  Output                          │
├──────────┤                                  │
│ cmd 1    │  <streams here when running,     │
│ cmd 2    │   or shows historical output     │
│ cmd 3 ◀  │   when a past command is         │
│ cmd 4    │   selected>                      │
│          │                                  │
├──────────┴──────────────────────────────────┤
│ > [ input ...                       ] [Run] │  ← locked when busy, with [Stop]
└─────────────────────────────────────────────┘
```

- While a command is running, the right pane shows that command's live output. History items are visible in the left list but are not selectable (clicking does nothing). Input is locked.
- While idle, the right pane shows whichever history item is selected (or an empty state if none). Input is unlocked.
- Connection status indicator in the header: green / yellow (reconnecting) / red.
- On 401, redirect to login.

## 12. Testing strategy

### Unit tests
- Go (`go test ./...`): `shell.Broadcaster` fan-out correctness under slow subscribers; `store` atomic write semantics; `auth` token comparison constant-time.
- React (`vitest`): `useShell` state machine (idle → running → idle, reattach handling).

### Integration tests
- Go: spin up `shell + store` against a tmp dir, send commands, assert files and broadcaster output. No HTTP, no k8s.

### E2E tests (kind)

Local infra:
```
test/e2e/
  setup.sh        # kind create cluster + kind load docker-image + kubectl apply
                  # kubectl port-forward svc/alfred 18080:8080 (background)
  teardown.sh
  e2e_test.go     # drives http://localhost:18080
```

Skip ingress / TLS in E2E (kind has no real DNS, Let's Encrypt cannot issue). Test through `Service` via `port-forward`. Ingress correctness verified manually in production.

Scenarios:

1. `RunSimpleCommand` — `echo` returns expected output, JSON file created.
2. `RunSlowCommand_StreamingOutput` — `for i in 1 2 3; do echo $i; sleep 1; done`. Assert chunks arrive incrementally, not all at the end.
3. `CDPersistsAcrossCommands` — `cd /tmp` then `pwd` returns `/tmp`. Proves persistent shell state.
4. `DisconnectReconnect_PicksUpRunningCommand` — start `sleep 10 && echo done`, disconnect WS at t=2s, reconnect at t=5s, assert `reattach` event with `outputSoFar`, then receive `done` event with expected final output.
5. `WrongPassword_Rejected` — 401 on wrong credentials, then 429 after 5 attempts.
6. `NoToken_WSRejected` — WS upgrade without `?token=` returns 401, no upgrade.
7. `StopRunningCommand` — send `sleep 100`, call `POST /api/commands/:id/stop`, assert command ends within 5 s with non-zero exit and `status:"stopped"`.

### CI

GitHub Actions: `helm/kind-action` to provision kind, run unit + integration on push, run E2E on PR.

## 13. Deployment

- Image: `ghcr.io/<owner>/headless-alfred:<git-sha>`
- Single Deployment, 1 replica (PVC is RWO; bash state is process-local — multiple replicas would be incoherent)
- `restartPolicy: Always`, no PodDisruptionBudget needed for single-instance personal use
- DNS: `agent.jesseliu.me` A record → cluster node public IP
- Ingress: Traefik with `cert-manager.io/cluster-issuer: letsencrypt-prod` annotation
- Secret created out-of-band: `kubectl create secret generic alfred-secret --from-literal=ALFRED_USER=... --from-literal=ALFRED_PASSWORD=... --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)`

## 14. Future (explicitly out of scope for v1)

- Multi-session / tabs (schema already reserves room: each JSON record could grow a `session_id` field)
- Full PTY mode (interactive TUIs)
- Embedded controls for Claude Code (the original Headless-Alfred motivation)
- Search across history output
- Output download as file
- Prometheus metrics at `/metrics`

These should be added by extension, not by rewriting v1.
