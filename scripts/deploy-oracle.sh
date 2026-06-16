#!/usr/bin/env bash
# Helm-install the Alfred chart into the oracle k3s.
# Assumes scripts/build-on-oracle.sh has already populated the
# k3s containerd k8s.io namespace with alfred/headless-alfred:local.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

NAMESPACE="${NAMESPACE:-alfred}"
RELEASE="${RELEASE:-alfred}"

# All three are required. Fail loud rather than silently ship the
# values.yaml REPLACE_ME defaults (which the chart will accept and
# load as literal auth credentials — that's a P0 if it ever happens).
for var in ALFRED_USER ALFRED_PASSWORD ALFRED_TOKEN; do
    if [[ -z "${!var:-}" ]]; then
        echo "ERROR: $var env var must be set." >&2
        echo "       For CI: comes from the matching GitHub Secret." >&2
        echo "       Manual: $var='...' bash scripts/deploy-oracle.sh" >&2
        exit 1
    fi
done

echo "==> Helm deploy → oracle k3s"
echo "    release:   $RELEASE"
echo "    namespace: $NAMESPACE"

helm upgrade --install "$RELEASE" deploy/helm/alfred \
    --namespace "$NAMESPACE" \
    --create-namespace \
    -f deploy/helm/alfred/values.yaml \
    -f deploy/helm/alfred/values-oracle.yaml \
    --set "auth.user=$ALFRED_USER" \
    --set "auth.password=$ALFRED_PASSWORD" \
    --set "auth.token=$ALFRED_TOKEN" \
    --wait --timeout 5m

echo
echo "==> Release status"
kubectl -n "$NAMESPACE" get pods,svc,ingress,pvc
