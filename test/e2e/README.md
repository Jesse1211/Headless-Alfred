# Headless Alfred — E2E

Drives the full stack inside a local **kind** cluster. Tests the same
manifests that ship to k3s, minus Ingress (kind has no real DNS).

## Prerequisites

- Docker (running)
- [kind](https://kind.sigs.k8s.io/) ≥ 0.20 (`brew install kind`)
- kubectl

## Once per session

```bash
make e2e-setup
```

This:
1. Builds image `headless-alfred:e2e` locally (from your current working tree)
2. Creates a kind cluster named **`alfred-e2e`** if one doesn't exist
3. Loads the image into the cluster
4. Applies namespace / pvc / service / patched-deployment (skips Ingress)
5. Waits for the Pod to be ready
6. Starts a background `kubectl port-forward` on http://127.0.0.1:18080

Takes ~90 s on a cold cache, ~30 s on a warm one.

## Run tests

```bash
make e2e
```

Iterate freely — the cluster stays between runs. The tests are tagged
`//go:build e2e` so they don't run during normal `go test`.

## Tear it all down

```bash
make e2e-teardown
```

Removes only the `alfred-e2e` kind cluster; any other kind clusters on your
machine (e.g. ones from other projects) are untouched.

## Tweak credentials

Defaults: `admin` / `e2etest` / `e2etesttoken`. Override via env at setup time:

```bash
TEST_PASSWORD=mypw TEST_TOKEN=mytok make e2e-setup
TEST_PASSWORD=mypw TEST_TOKEN=mytok make e2e
```

## Common quirks

- **The 429 rate-limit test is a soft assertion.** The login limiter is
  per-IP per-Pod and may have been drained by a previous test run; the test
  logs but does not fail in that case.

- **`kubectl exec` checks are best-effort.** The first scenario reads
  `/data/commands/<id>.json` from inside the Pod via `kubectl exec` to
  confirm the record persisted. If kubectl can't reach the cluster (CI
  without context), the check returns `""` and is silently skipped.

- **If the port-forward dies.** Re-run `make e2e-setup` — it kills any
  stale port-forward and starts a fresh one.

- **Reusing the cluster across days.** The PVC keeps history. To start fresh,
  `make e2e-teardown && make e2e-setup`.

## What's tested

| # | Scenario | Maps to spec §12 line |
|---|---|---|
| 1 | `RunSimpleCommand` — echo round-trips, JSON record persists | 1 |
| 2 | `RunSlowCommand_StreamingOutput` — chunks arrive over time | 2 |
| 3 | `CDPersistsAcrossCommands` — cd /tmp then pwd | 3 |
| 4 | `DisconnectReconnect_PicksUpRunningCommand` — reattach event delivers in-flight cmd | 4 |
| 5 | `WrongPassword_Rejected` — 401 then 429 after 5 | 5 |
| 6 | `NoToken_WSRejected` — WS upgrade with no `?token=` returns 401 | 6 |
| 7 | `StopRunningCommand` — REST stop kills bash, command ends non-zero | 7 |
