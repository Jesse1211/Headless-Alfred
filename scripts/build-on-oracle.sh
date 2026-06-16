#!/usr/bin/env bash
# Build the Alfred image directly into the k3s containerd k8s.io
# namespace via nerdctl + buildkit. No registry, no docker daemon.
# The resulting alfred/headless-alfred:local image is immediately
# usable from kubelet with imagePullPolicy: Never.
#
# Run from the repo root on the oracle box (or via the GitHub Actions
# workflow which sshs in and runs this).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

IMG="alfred/headless-alfred:local"

# CLAUDE_CLI_VERSION can be overridden if you want to pin a different
# CLI release for a one-off build. Defaults match the Dockerfile ARG.
CLAUDE_CLI_VERSION="${CLAUDE_CLI_VERSION:-2.1.142}"

echo "==> Building $IMG into k3s containerd (k8s.io namespace)"
echo "    claude-cli: $CLAUDE_CLI_VERSION"

nerdctl --namespace=k8s.io build \
    --build-arg "CLAUDE_CLI_VERSION=${CLAUDE_CLI_VERSION}" \
    --tag "$IMG" \
    --file Dockerfile \
    .

echo
echo "==> Verifying image in k3s containerd"
nerdctl --namespace=k8s.io image ls | grep '^alfred/headless-alfred' || {
    echo "FAIL: alfred/headless-alfred:local not visible to k3s containerd" >&2
    exit 1
}
