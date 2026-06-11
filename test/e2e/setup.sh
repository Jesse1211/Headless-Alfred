#!/usr/bin/env bash
# Provision a kind cluster named "alfred-e2e", build the alfred image locally,
# load it into the kind nodes, deploy alfred (skipping Ingress), and start a
# kubectl port-forward on localhost:18080. Idempotent: if the cluster already
# exists, reuses it and reapplies manifests.
#
# Usage: ./test/e2e/setup.sh
# Env:
#   IMAGE_TAG=...         (default: e2e)
#   LOCAL_PORT=...        (default: 18080)
#   TEST_USER/PASSWORD/TOKEN (defaults below)

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
command -v kind >/dev/null || { echo "kind not installed (brew install kind)"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl not installed"; exit 1; }
command -v docker >/dev/null || { echo "docker not installed"; exit 1; }

# 1. Build the image locally with a plain "headless-alfred:<tag>" name so the
#    kind load step doesn't need to retag.
echo "[setup] building image ${IMAGE}…"
docker build -t "$IMAGE" .

# 2. Create cluster if missing.
if ! kind get clusters | grep -qx "$CLUSTER_NAME"; then
  echo "[setup] creating kind cluster ${CLUSTER_NAME}…"
  kind create cluster --name "$CLUSTER_NAME" --config test/e2e/kind-config.yaml
else
  echo "[setup] reusing existing kind cluster $CLUSTER_NAME"
fi

# 3. Load image into kind.
echo "[setup] loading image into kind…"
kind load docker-image --name "$CLUSTER_NAME" "$IMAGE"

# 4. Apply Namespace, then re-create Secret with known test values, then PVC,
#    Service, and a patched Deployment that references our locally-loaded image.
#    Skip Ingress entirely (no DNS, no LE in kind).
echo "[setup] applying manifests…"
kubectl apply -f deploy/manifests/namespace.yaml

kubectl -n "$NS" delete secret alfred-secret --ignore-not-found
kubectl -n "$NS" create secret generic alfred-secret \
  --from-literal=ALFRED_USER="$TEST_USER" \
  --from-literal=ALFRED_PASSWORD="$TEST_PASSWORD" \
  --from-literal=ALFRED_TOKEN="$TEST_TOKEN"

kubectl apply -f deploy/manifests/pvc.yaml
kubectl apply -f deploy/manifests/service.yaml

# Patch the Deployment to reference the locally-loaded image tag.
TMP_DEP="$(mktemp)"
sed -e "s|ghcr.io/jesseliu/headless-alfred:dev|$IMAGE|g" \
  deploy/manifests/deployment.yaml > "$TMP_DEP"
kubectl apply -f "$TMP_DEP"
rm -f "$TMP_DEP"

# 5. Wait for the Pod to be ready (up to 2 min).
echo "[setup] waiting for pod readiness…"
kubectl -n "$NS" rollout status deployment/alfred --timeout=120s

# 6. Kill any previous port-forward on our port.
if [ -f /tmp/alfred-e2e-pf.pid ]; then
  OLD_PID=$(cat /tmp/alfred-e2e-pf.pid)
  if kill -0 "$OLD_PID" 2>/dev/null; then
    kill "$OLD_PID" 2>/dev/null || true
  fi
  rm -f /tmp/alfred-e2e-pf.pid
fi
pkill -f "kubectl.*port-forward.*svc/alfred.*$LOCAL_PORT" 2>/dev/null || true
sleep 1

# 7. Start port-forward in background.
echo "[setup] starting port-forward on :${LOCAL_PORT}…"
kubectl -n "$NS" port-forward svc/alfred "$LOCAL_PORT:8080" \
  > /tmp/alfred-e2e-pf.log 2>&1 &
PF_PID=$!
echo "$PF_PID" > /tmp/alfred-e2e-pf.pid
disown $PF_PID 2>/dev/null || true

# 8. Wait until port-forward is reachable.
for i in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:$LOCAL_PORT/readyz" >/dev/null 2>&1; then
    echo "[setup] backend reachable at http://127.0.0.1:$LOCAL_PORT"
    echo "[setup] DONE"
    exit 0
  fi
  sleep 0.2
done

echo "[setup] backend did not become reachable; port-forward log:"
cat /tmp/alfred-e2e-pf.log
exit 1
