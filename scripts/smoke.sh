#!/usr/bin/env bash
# Smoke-test the alfred-server binary end-to-end against a real local instance.
# Boots the server in the background with throwaway env, hits the REST surface,
# verifies expected codes/bodies, then tears down.
#
# Usage: ./scripts/smoke.sh
# Assumes: ./bin/alfred-server already built (`make build` or `go build`).

set -euo pipefail

DATA=$(mktemp -d)
PORT=18080
BASE="http://127.0.0.1:${PORT}"

cleanup() {
  if [ -n "${SERVER_PID:-}" ]; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -rf "${DATA}"
}
trap cleanup EXIT

export ALFRED_USER=admin
export ALFRED_PASSWORD=test
export ALFRED_TOKEN=smoketoken
export ALFRED_DATA_DIR="${DATA}"
export ALFRED_ADDR="127.0.0.1:${PORT}"

./bin/alfred-server >"${DATA}/server.log" 2>&1 &
SERVER_PID=$!

# Wait until /readyz is up (max ~5s).
for i in $(seq 1 50); do
  if curl -sf "${BASE}/readyz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
if ! curl -sf "${BASE}/readyz" >/dev/null 2>&1; then
  echo "FAIL: server never became ready"
  cat "${DATA}/server.log"
  exit 1
fi

# /healthz should also return ok.
curl -sf "${BASE}/healthz" >/dev/null || { echo "FAIL: /healthz"; exit 1; }

# Login with correct creds.
TOKEN=$(curl -sf -X POST "${BASE}/api/login" \
  -H 'Content-Type: application/json' \
  -d '{"user":"admin","password":"test"}' | grep -oE '"token":"[^"]+"' | cut -d'"' -f4)
if [ "${TOKEN}" != "smoketoken" ]; then
  echo "FAIL: login token mismatch: got '${TOKEN}'"
  exit 1
fi

# Login with wrong password should be 401.
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/api/login" \
  -H 'Content-Type: application/json' \
  -d '{"user":"admin","password":"WRONG"}')
if [ "${CODE}" != "401" ]; then
  echo "FAIL: wrong-password code = ${CODE}, want 401"
  exit 1
fi

# Authenticated request without token → 401.
CODE=$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/api/sessions")
if [ "${CODE}" != "401" ]; then
  echo "FAIL: unauth /api/sessions code = ${CODE}, want 401"
  exit 1
fi

# Sessions list with token → 200 + empty array (fresh data dir).
BODY=$(curl -sf "${BASE}/api/sessions" -H "Authorization: Bearer ${TOKEN}")
if ! echo "${BODY}" | grep -qE '^\[\]'; then
  echo "FAIL: /api/sessions empty body = '${BODY}'"
  exit 1
fi

# Create a session, then list its (empty) commands.
SID=$(curl -sf -X POST "${BASE}/api/sessions" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"smoke"}' | grep -oE '"id":"[^"]+"' | head -1 | cut -d'"' -f4)
if [ -z "${SID}" ]; then
  echo "FAIL: create session returned no id"
  exit 1
fi

BODY=$(curl -sf "${BASE}/api/sessions/${SID}/commands" -H "Authorization: Bearer ${TOKEN}")
if ! echo "${BODY}" | grep -qE '^\[\]'; then
  echo "FAIL: /api/sessions/${SID}/commands empty body = '${BODY}'"
  exit 1
fi

# 404 for unknown command id within the session.
CODE=$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/api/sessions/${SID}/commands/nope" -H "Authorization: Bearer ${TOKEN}")
if [ "${CODE}" != "404" ]; then
  echo "FAIL: missing command code = ${CODE}, want 404"
  exit 1
fi

echo "smoke OK"
