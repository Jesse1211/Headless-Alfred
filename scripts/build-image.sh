#!/usr/bin/env bash
# Build the Headless Alfred container image.
#
# Usage:
#   scripts/build-image.sh                    # build with tag :dev
#   scripts/build-image.sh -t v1.2.3          # tag :v1.2.3 (and :latest)
#   scripts/build-image.sh -r ghcr.io/me -t v1.2.3
#   scripts/build-image.sh --push -r ghcr.io/me -t v1.2.3
#
# Flags:
#   -t, --tag      image tag (default: dev or short-sha)
#   -r, --registry registry prefix (default: ghcr.io/jesseliu)
#   --push         also push after build (requires docker login)
#   --platform     comma-separated target platforms (default: linux/amd64)
#   -h, --help     show this help and exit

set -euo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || { cd "$(dirname "$0")/.." && pwd; })"

REGISTRY="${ALFRED_REGISTRY:-ghcr.io/jesseliu}"
NAME="headless-alfred"
TAG=""
PUSH=0
PLATFORM="linux/amd64"

usage() {
  sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -t|--tag)      TAG="$2"; shift 2 ;;
    -r|--registry) REGISTRY="$2"; shift 2 ;;
    --push)        PUSH=1; shift ;;
    --platform)    PLATFORM="$2"; shift 2 ;;
    -h|--help)     usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

# Default tag: short git SHA when in a repo with commits, else "dev".
if [[ -z "${TAG}" ]]; then
  if SHA=$(git rev-parse --short HEAD 2>/dev/null); then
    TAG="${SHA}"
  else
    TAG="dev"
  fi
fi

FULL="${REGISTRY}/${NAME}:${TAG}"
LATEST="${REGISTRY}/${NAME}:latest"

echo "Building ${FULL}"
echo "         ${LATEST}"
echo "platform=${PLATFORM} version=${TAG}"
echo

# Use buildx if available for multi-platform; fall back to plain build for single-platform.
if [[ "${PLATFORM}" == *,* ]]; then
  CMD=(docker buildx build --platform "${PLATFORM}")
  if [[ ${PUSH} -eq 1 ]]; then
    CMD+=(--push)
  else
    CMD+=(--load)
    if [[ "${PLATFORM}" == *,* ]]; then
      echo "warning: multi-platform build without --push will not load into local Docker" >&2
    fi
  fi
else
  CMD=(docker build)
fi

"${CMD[@]}" \
  --build-arg "VERSION=${TAG}" \
  -t "${FULL}" \
  -t "${LATEST}" \
  -f Dockerfile \
  .

if [[ ${PUSH} -eq 1 && ! "${PLATFORM}" == *,* ]]; then
  echo "Pushing ${FULL}"
  docker push "${FULL}"
  docker push "${LATEST}"
fi

echo
echo "OK: ${FULL}"
if [[ ${PUSH} -eq 0 ]]; then
  echo "(not pushed — pass --push to publish)"
fi
