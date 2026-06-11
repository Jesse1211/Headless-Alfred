#!/usr/bin/env bash
# Stop the port-forward and delete the alfred-e2e kind cluster.
# Leaves any other kind clusters untouched.

set -euo pipefail

CLUSTER_NAME="alfred-e2e"

# Stop port-forward if running.
if [ -f /tmp/alfred-e2e-pf.pid ]; then
  PID=$(cat /tmp/alfred-e2e-pf.pid)
  kill "$PID" 2>/dev/null || true
  rm -f /tmp/alfred-e2e-pf.pid /tmp/alfred-e2e-pf.log
fi

# Belt and braces in case the pid file was lost.
pkill -f "kubectl.*port-forward.*svc/alfred" 2>/dev/null || true

# Delete only OUR cluster.
if kind get clusters | grep -qx "$CLUSTER_NAME"; then
  echo "[teardown] deleting kind cluster $CLUSTER_NAME"
  kind delete cluster --name "$CLUSTER_NAME"
else
  echo "[teardown] cluster $CLUSTER_NAME does not exist; skipping delete"
fi

echo "[teardown] DONE"
