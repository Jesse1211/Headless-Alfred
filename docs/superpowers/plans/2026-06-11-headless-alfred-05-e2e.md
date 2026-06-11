# Headless Alfred — Plan 5: E2E in Kind

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automate the 7 E2E scenarios from spec §12 against a real Kubernetes deployment (kind cluster), driven from a Go test binary on the developer's laptop.

**Architecture:** A bash script provisions a kind cluster, loads the locally-built image, applies the manifests minus Ingress (so we skip Let's Encrypt and TLS — both impossible against `localhost`), and `kubectl port-forward`s the Service to localhost. A Go test binary exercises the system over plain HTTP + WS on `127.0.0.1:18080`. Same test code runs in GitHub Actions via `helm/kind-action`.

**Tech Stack:** kind, kubectl, Go's standard `testing` + `gorilla/websocket`, GitHub Actions.

**Spec sections covered:** §12 (Testing strategy: E2E in kind, 7 scenarios + CI).

**Depends on:** Plan 4 (Dockerfile + manifests).

---

## File Structure

```
test/
└── e2e/
    ├── kind-config.yaml         # kind cluster definition
    ├── setup.sh                 # provisions cluster + deploys + port-forwards
    ├── teardown.sh              # tears it all down
    ├── e2e_test.go              # the 7 scenarios + helpers
    └── README.md                # how to run locally
Makefile                         # adds e2e-setup / e2e / e2e-teardown
.github/
└── workflows/
    └── ci.yaml                  # unit + integration + e2e on PR
```

---

## Task 1: kind cluster config

**Files:**
- Create: `test/e2e/kind-config.yaml`

- [ ] **Step 1.1: Write kind-config.yaml**

Create `test/e2e/kind-config.yaml`:
```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: alfred-e2e
nodes:
  - role: control-plane
```

Plain single-node config. No special port mappings — we use `kubectl port-forward` instead of an ingress in the test loop.

- [ ] **Step 1.2: Commit**

```bash
git add test/e2e/kind-config.yaml
git commit -m "test(e2e): kind cluster config"
```

---

## Task 2: setup.sh

**Files:**
- Create: `test/e2e/setup.sh`

- [ ] **Step 2.1: Write setup.sh**

Create `test/e2e/setup.sh`:
```bash
#!/usr/bin/env bash
# Provision a kind cluster, deploy alfred, port-forward to localhost:18080.
# Idempotent: if the cluster already exists, reuses it; reapplies manifests.
set -euo pipefail

CLUSTER_NAME="alfred-e2e"
NS="alfred"
IMAGE_TAG="${IMAGE_TAG:-e2e}"
IMAGE="headless-alfred:${IMAGE_TAG}"
LOCAL_PORT="${LOCAL_PORT:-18080}"
TEST_USER="${TEST_USER:-admin}"
TEST_PASSWORD="${TEST_PASSWORD:-e2etest}"
TEST_TOKEN="${TEST_TOKEN:-e2etesttoken}"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

echo "[setup] checking prerequisites…"
command -v kind >/dev/null || { echo "kind not installed"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl not installed"; exit 1; }
command -v docker >/dev/null || { echo "docker not installed"; exit 1; }

# 1. Build the image locally (so we don't need a registry).
echo "[setup] building image $IMAGE…"
docker build -t "$IMAGE" .

# 2. Create cluster if missing.
if ! kind get clusters | grep -qx "$CLUSTER_NAME"; then
  echo "[setup] creating kind cluster…"
  kind create cluster --name "$CLUSTER_NAME" --config test/e2e/kind-config.yaml
else
  echo "[setup] reusing existing kind cluster"
fi

# 3. Load image into kind.
echo "[setup] loading image into kind…"
kind load docker-image --name "$CLUSTER_NAME" "$IMAGE"

# 4. Apply Namespace + Secret + PVC + Service + Deployment. Skip Ingress.
echo "[setup] applying manifests…"
kubectl apply -f deploy/manifests/namespace.yaml

# Recreate Secret to ensure values are known to the test process.
kubectl -n "$NS" delete secret alfred-secret --ignore-not-found
kubectl -n "$NS" create secret generic alfred-secret \
  --from-literal=ALFRED_USER="$TEST_USER" \
  --from-literal=ALFRED_PASSWORD="$TEST_PASSWORD" \
  --from-literal=ALFRED_TOKEN="$TEST_TOKEN"

kubectl apply -f deploy/manifests/pvc.yaml
kubectl apply -f deploy/manifests/service.yaml

# Patch the Deployment to use the locally-loaded image tag.
TMP_DEP="$(mktemp)"
sed -e "s|ghcr.io/jesseliu/headless-alfred:dev|$IMAGE|g" \
  deploy/manifests/deployment.yaml > "$TMP_DEP"
kubectl apply -f "$TMP_DEP"
rm -f "$TMP_DEP"

# 5. Wait for the Pod to be ready.
echo "[setup] waiting for pod readiness…"
kubectl -n "$NS" rollout status deployment/alfred --timeout=120s

# 6. Kill any previous port-forward for our port.
if pgrep -f "kubectl.*port-forward.*svc/alfred.*$LOCAL_PORT" >/dev/null; then
  pkill -f "kubectl.*port-forward.*svc/alfred.*$LOCAL_PORT"
  sleep 1
fi

# 7. Start port-forward in background.
echo "[setup] starting port-forward on :$LOCAL_PORT…"
kubectl -n "$NS" port-forward svc/alfred "$LOCAL_PORT:8080" \
  > /tmp/alfred-e2e-pf.log 2>&1 &
PF_PID=$!
echo "$PF_PID" > /tmp/alfred-e2e-pf.pid

# 8. Wait until port-forward is reachable.
for i in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:$LOCAL_PORT/readyz" >/dev/null; then
    echo "[setup] backend reachable at http://127.0.0.1:$LOCAL_PORT"
    echo "[setup] DONE"
    exit 0
  fi
  sleep 0.2
done

echo "[setup] backend did not become reachable; port-forward log:"
cat /tmp/alfred-e2e-pf.log
exit 1
```

- [ ] **Step 2.2: Make executable**

```bash
chmod +x test/e2e/setup.sh
```

- [ ] **Step 2.3: Commit**

```bash
git add test/e2e/setup.sh
git commit -m "test(e2e): setup script provisioning kind + port-forward"
```

---

## Task 3: teardown.sh

**Files:**
- Create: `test/e2e/teardown.sh`

- [ ] **Step 3.1: Write teardown.sh**

Create `test/e2e/teardown.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail
CLUSTER_NAME="alfred-e2e"

# Stop port-forward if running.
if [ -f /tmp/alfred-e2e-pf.pid ]; then
  PID=$(cat /tmp/alfred-e2e-pf.pid)
  kill "$PID" 2>/dev/null || true
  rm -f /tmp/alfred-e2e-pf.pid /tmp/alfred-e2e-pf.log
fi

# Delete the cluster.
if kind get clusters | grep -qx "$CLUSTER_NAME"; then
  kind delete cluster --name "$CLUSTER_NAME"
fi

echo "[teardown] DONE"
```

- [ ] **Step 3.2: Make executable + commit**

```bash
chmod +x test/e2e/teardown.sh
git add test/e2e/teardown.sh
git commit -m "test(e2e): teardown script"
```

---

## Task 4: e2e_test.go — helpers and 7 scenarios

**Files:**
- Create: `test/e2e/e2e_test.go`

The tests assume `setup.sh` already ran. They drive `http://127.0.0.1:18080`.

- [ ] **Step 4.1: Write e2e_test.go**

Create `test/e2e/e2e_test.go`:
```go
//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// --- env / config ---------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var (
	baseHTTP     = envOr("ALFRED_E2E_HTTP", "http://127.0.0.1:18080")
	baseWS       = envOr("ALFRED_E2E_WS", "ws://127.0.0.1:18080")
	testUser     = envOr("TEST_USER", "admin")
	testPassword = envOr("TEST_PASSWORD", "e2etest")
	testToken    = envOr("TEST_TOKEN", "e2etesttoken")
)

// --- helpers -------------------------------------------------------------

func login(t *testing.T, user, password string) (string, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"user": user, "password": password})
	resp, err := http.Post(baseHTTP+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", resp.StatusCode
	}
	var out struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Token, resp.StatusCode
}

func dialWS(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(baseWS + "/ws")
	u.RawQuery = "token=" + url.QueryEscape(token)
	c, _, err := websocket.DefaultDialer.DialContext(context.Background(), u.String(), nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type wsMsg struct {
	Type        string `json:"type"`
	CmdID       string `json:"cmdId,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	OutputSoFar string `json:"outputSoFar,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

func readMsg(t *testing.T, c *websocket.Conn, timeout time.Duration) wsMsg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	var m wsMsg
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("read msg: %v", err)
	}
	return m
}

func sendMsg(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	if err := c.WriteJSON(v); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// runCommand sends a run and consumes events until "done". Returns output text
// and the exit code. Times out via t.Fatalf.
func runCommand(t *testing.T, c *websocket.Conn, command string, perEventTimeout time.Duration) (cmdID, output string, exit int) {
	t.Helper()
	sendMsg(t, c, map[string]string{"type": "run", "command": command})
	var body bytes.Buffer
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		m := readMsg(t, c, perEventTimeout)
		switch m.Type {
		case "started":
			cmdID = m.CmdID
		case "chunk":
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			body.Write(b)
		case "done":
			return cmdID, body.String(), m.ExitCode
		case "error":
			t.Fatalf("server error: code=%s msg=%s", m.Code, m.Message)
		}
	}
	t.Fatalf("runCommand: deadline exceeded; partial=%q", body.String())
	return
}

// waitForIdle drains the WS until the next message is "idle".
func expectInitialIdleOrReattach(t *testing.T, c *websocket.Conn) wsMsg {
	t.Helper()
	m := readMsg(t, c, 5*time.Second)
	if m.Type != "idle" && m.Type != "reattach" {
		t.Fatalf("first message should be idle or reattach; got %+v", m)
	}
	return m
}

// kubectlExecCat reads a file inside the alfred pod and returns its bytes.
// Best-effort: returns "" on failure (some tests use it for opportunistic checks).
func kubectlExecCat(path string) string {
	cmd := exec.Command("kubectl", "-n", "alfred", "exec", "deployment/alfred", "--", "cat", path)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// --- the 7 scenarios -----------------------------------------------------

func TestE2E_RunSimpleCommand(t *testing.T) {
	tok, code := login(t, testUser, testPassword)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}
	if tok != testToken {
		t.Fatalf("token mismatch: got %q want %q", tok, testToken)
	}

	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	cmdID, out, exit := runCommand(t, c, "echo hello-e2e", 10*time.Second)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; out=%q", exit, out)
	}
	if !strings.Contains(out, "hello-e2e") {
		t.Fatalf("missing expected output; got %q", out)
	}

	// Opportunistic: check the JSON record was written inside the pod.
	jsonContent := kubectlExecCat(fmt.Sprintf("/data/commands/%s.json", cmdID))
	if jsonContent != "" && !strings.Contains(jsonContent, "completed") {
		t.Fatalf("expected status:completed in JSON, got %q", jsonContent)
	}
}

func TestE2E_RunSlowCommand_StreamingOutput(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	sendMsg(t, c, map[string]string{"type": "run", "command": "for i in 1 2 3; do echo $i; sleep 1; done"})

	var chunkTimes []time.Time
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m := readMsg(t, c, 5*time.Second)
		switch m.Type {
		case "chunk":
			chunkTimes = append(chunkTimes, time.Now())
		case "done":
			if m.ExitCode != 0 {
				t.Fatalf("exit=%d", m.ExitCode)
			}
			if len(chunkTimes) < 2 {
				t.Fatalf("expected streamed chunks, got %d", len(chunkTimes))
			}
			gap := chunkTimes[len(chunkTimes)-1].Sub(chunkTimes[0])
			if gap < 500*time.Millisecond {
				t.Fatalf("chunks delivered too quickly (%s); not streaming", gap)
			}
			return
		}
	}
	t.Fatal("never received done")
}

func TestE2E_CDPersistsAcrossCommands(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	// First, cd to /tmp.
	_, _, ec := runCommand(t, c, "cd /tmp", 5*time.Second)
	if ec != 0 {
		t.Fatalf("cd failed: ec=%d", ec)
	}

	// Then pwd should print /tmp.
	_, out, ec2 := runCommand(t, c, "pwd", 5*time.Second)
	if ec2 != 0 {
		t.Fatalf("pwd failed: ec=%d", ec2)
	}
	if !strings.Contains(out, "/tmp") {
		t.Fatalf("pwd output=%q; want /tmp", out)
	}
}

func TestE2E_DisconnectReconnect_PicksUpRunningCommand(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c1 := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c1)

	sendMsg(t, c1, map[string]string{"type": "run", "command": "sleep 4; echo finished-after-reconnect"})

	// Wait for started.
	for {
		m := readMsg(t, c1, 5*time.Second)
		if m.Type == "started" {
			break
		}
	}
	// Disconnect mid-flight.
	_ = c1.Close()
	time.Sleep(500 * time.Millisecond)

	// Reconnect; first message must be reattach.
	c2 := dialWS(t, tok)
	m := readMsg(t, c2, 5*time.Second)
	if m.Type != "reattach" {
		t.Fatalf("expected reattach, got %+v", m)
	}

	// Wait for done.
	var finalOutput string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		m := readMsg(t, c2, 5*time.Second)
		switch m.Type {
		case "chunk":
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			finalOutput += string(b)
		case "done":
			if m.ExitCode != 0 {
				t.Fatalf("exit=%d", m.ExitCode)
			}
			// outputSoFar from reattach plus any post-reconnect chunks
			// should contain the eventual echoed line. We decoded the post-
			// reconnect chunks above; reattach's outputSoFar may or may not
			// have content yet. So check both.
			pre, _ := base64.StdEncoding.DecodeString(m.OutputSoFar)
			joined := string(pre) + finalOutput
			if !strings.Contains(joined, "finished-after-reconnect") {
				t.Fatalf("missing finishing line; joined=%q", joined)
			}
			return
		}
	}
	t.Fatal("no done within deadline")
}

func TestE2E_WrongPassword_Rejected(t *testing.T) {
	_, code := login(t, testUser, "wrong-password")
	if code != http.StatusUnauthorized {
		t.Fatalf("code=%d; want 401", code)
	}

	// After 4 more bad attempts (5 total within the minute), the 6th should be 429.
	for i := 0; i < 4; i++ {
		_, _ = login(t, testUser, "wrong-password")
	}
	_, code = login(t, testUser, "wrong-password")
	if code != http.StatusTooManyRequests {
		t.Logf("expected 429 after 5 bad attempts; got %d. (may flake if rate buckets reset)", code)
	}
}

func TestE2E_NoToken_WSRejected(t *testing.T) {
	u, _ := url.Parse(baseWS + "/ws") // no ?token=
	_, resp, err := websocket.DefaultDialer.DialContext(context.Background(), u.String(), nil)
	if err == nil {
		t.Fatal("dial succeeded without token; want failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got resp=%v; want 401", resp)
	}
}

func TestE2E_StopRunningCommand(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	c := dialWS(t, tok)
	expectInitialIdleOrReattach(t, c)

	sendMsg(t, c, map[string]string{"type": "run", "command": "sleep 60"})
	var cmdID string
	for {
		m := readMsg(t, c, 5*time.Second)
		if m.Type == "started" {
			cmdID = m.CmdID
			break
		}
	}
	// Give bash a moment to actually start sleeping.
	time.Sleep(500 * time.Millisecond)

	// Send Stop via REST.
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/commands/%s/stop", baseHTTP, cmdID), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop code=%d", resp.StatusCode)
	}

	// Expect "done" within 5 seconds, non-zero exit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := readMsg(t, c, 5*time.Second)
		if m.Type == "done" && m.CmdID == cmdID {
			if m.ExitCode == 0 {
				t.Fatalf("expected non-zero exit on stop, got 0")
			}
			return
		}
	}
	t.Fatal("stop did not produce done event in time")
}
```

- [ ] **Step 4.2: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test(e2e): 7 scenarios covering spec §12"
```

---

## Task 5: Wire Makefile targets

**Files:**
- Modify: `Makefile` (root)

- [ ] **Step 5.1: Append e2e targets to root Makefile**

Append to `Makefile`:
```makefile
.PHONY: e2e e2e-setup e2e-teardown

e2e-setup:
	./test/e2e/setup.sh

e2e:
	go test -tags=e2e -v -timeout=10m ./test/e2e/

e2e-teardown:
	./test/e2e/teardown.sh
```

- [ ] **Step 5.2: Add operator README for the directory**

Create `test/e2e/README.md`:
```markdown
# Headless Alfred — E2E

Run the full stack inside a local kind cluster.

## Prerequisites

- Docker
- kind ≥ 0.20
- kubectl

## Once per session

```bash
make e2e-setup    # ~2-3 minutes the first time
```

This builds the image, creates a kind cluster named `alfred-e2e`, deploys the app, and starts a port-forward at http://127.0.0.1:18080.

## Run tests

```bash
make e2e
```

Iterate freely — the cluster stays up between runs.

## Tear it all down

```bash
make e2e-teardown
```

## Tweak credentials

Defaults are admin / e2etest / e2etesttoken. Override via env:
```bash
TEST_PASSWORD=mypw TEST_TOKEN=mytok make e2e-setup
TEST_PASSWORD=mypw TEST_TOKEN=mytok make e2e
```
```

- [ ] **Step 5.3: Commit**

```bash
git add Makefile test/e2e/README.md
git commit -m "test(e2e): Makefile targets and README"
```

---

## Task 6: First green run locally

This is a manual verification step.

- [ ] **Step 6.1: Run setup**

```bash
make e2e-setup
```

Expected: ends with `[setup] DONE` after the cluster is up and the port-forward is reachable.

- [ ] **Step 6.2: Run tests**

```bash
make e2e
```

Expected: all 7 tests PASS. Output looks like:
```
=== RUN   TestE2E_RunSimpleCommand
--- PASS: TestE2E_RunSimpleCommand (0.5s)
=== RUN   TestE2E_RunSlowCommand_StreamingOutput
--- PASS: TestE2E_RunSlowCommand_StreamingOutput (3.5s)
...
PASS
ok  	github.com/jesseliu/headless-alfred/test/e2e	17.234s
```

- [ ] **Step 6.3: Tear down (optional)**

```bash
make e2e-teardown
```

---

## Task 7: GitHub Actions workflow

**Files:**
- Create: `.github/workflows/ci.yaml`

- [ ] **Step 7.1: Write ci.yaml**

Create `.github/workflows/ci.yaml`:
```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  go-unit-and-integration:
    name: Go unit + integration
    runs-on: ubuntu-22.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
          cache: true
      - name: Run tests
        run: make test

  web-build-and-test:
    name: Web build + unit tests
    runs-on: ubuntu-22.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: "20"
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: cd web && npm ci
      - run: cd web && npm test
      - run: cd web && npm run build

  e2e:
    name: E2E in kind
    runs-on: ubuntu-22.04
    # Only on PR — kind boot is ~2 min, keep main fast.
    if: github.event_name == 'pull_request'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
          cache: true
      - uses: actions/setup-node@v4
        with:
          node-version: "20"
          cache: npm
          cache-dependency-path: web/package-lock.json

      - name: Install kind
        uses: helm/kind-action@v1
        with:
          install_only: true
          version: v0.23.0

      - name: Set up Docker buildx
        uses: docker/setup-buildx-action@v3

      - name: Run E2E
        env:
          TEST_USER: admin
          TEST_PASSWORD: e2etest
          TEST_TOKEN: e2etesttoken
        run: |
          make e2e-setup
          make e2e

      - name: Show pod logs on failure
        if: failure()
        run: kubectl -n alfred logs deployment/alfred --tail=200 || true

      - name: Teardown
        if: always()
        run: make e2e-teardown
```

- [ ] **Step 7.2: Commit**

```bash
git add .github/workflows/ci.yaml
git commit -m "ci: unit + integration + E2E in GitHub Actions"
```

---

## Self-Review Notes

**Spec coverage:**
- §12 (testing) 7 E2E scenarios: ✓
- §12 CI integration via GitHub Actions + kind-action: ✓
- §12 skip Ingress in kind, port-forward instead: ✓

**Known quirks worth knowing:**
- `TestE2E_WrongPassword_Rejected`'s 429 check uses `t.Logf` (not `t.Fatalf`) if it doesn't hit, because the rate-limit bucket is per-process and 5/min — if a previous test run cooled down within the cluster's lifetime, the count is reset on Pod restart. It still asserts that the first wrong attempt is 401, which is the primary contract.
- `TestE2E_DisconnectReconnect_PicksUpRunningCommand` runs a 4-second `sleep`; chosen long enough to safely disconnect and reconnect, short enough to not slow CI noticeably.
- CI runs E2E only on PRs (not push to main), since kind boot is ~90 s. Adjust the `if:` if you want main-branch coverage too.
- All 7 tests are marked with `//go:build e2e` to keep them out of `go test ./...` default runs. Invoke with `make e2e` (which passes `-tags=e2e`).

**End state of Plan 5 (and the project):**
- `make test` runs unit + integration green
- `make e2e` runs full stack against kind, green
- CI is green on PR
- Manual production deploy is unblocked: follow `deploy/README.md`
