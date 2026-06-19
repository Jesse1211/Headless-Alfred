#!/usr/bin/env bash
# Kill any local alfred-server + Vite, rebuild from HEAD, and bring both
# 8080 (embedded prod-shape) and 5173 (Vite HMR dev) back up cleanly.
#
# Why this script exists: the two ports serve the SAME backend, but
# their FRONTEND comes from different places — 5173 reads `web/src/`
# live, 8080 reads the `internal/static/dist/` snapshot embedded in
# the Go binary at build time. A frontend change visible on 5173 is
# invisible on 8080 until you `npm run build` + copy + `go build` +
# restart. This script does all four for you so both URLs stay in
# sync.
#
# Usage:
#   ./scripts/local-dev.sh            # kill all, rebuild, start both
#   ./scripts/local-dev.sh --no-build # skip frontend rebuild (faster
#                                     # if you only touched Go code;
#                                     # 8080 will still serve the
#                                     # PREVIOUS embedded dist)
#   ./scripts/local-dev.sh --stop     # kill and exit (don't restart)
#
# Env (override defaults):
#   ALFRED_USER         (default: jesse)
#   ALFRED_PASSWORD     (default: Alfred.123...)
#   ALFRED_TOKEN        (default: local-dev-token-1234567890)
#   ALFRED_DATA_DIR     (default: /tmp/alfred-local-data)
#   ALFRED_SERVER_BIN   (default: /tmp/alfred-server)
#   ALFRED_SERVER_LOG   (default: /tmp/alfred-server.log)
#   VITE_LOG            (default: /tmp/vite.log)

set -euo pipefail

# Allow override; sensible defaults match what the team already uses.
USER_NAME="${ALFRED_USER:-jesse}"
PASSWORD="${ALFRED_PASSWORD:-Alfred.123...}"
TOKEN="${ALFRED_TOKEN:-local-dev-token-1234567890}"
DATA_DIR="${ALFRED_DATA_DIR:-/tmp/alfred-local-data}"
SERVER_BIN="${ALFRED_SERVER_BIN:-/tmp/alfred-server}"
SERVER_LOG="${ALFRED_SERVER_LOG:-/tmp/alfred-server.log}"
VITE_LOG="${VITE_LOG:-/tmp/vite.log}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

SKIP_BUILD=0
STOP_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --no-build) SKIP_BUILD=1 ;;
    --stop)     STOP_ONLY=1 ;;
    *)          echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

say() { printf '\033[1;34m▸\033[0m %s\n' "$*"; }
ok()  { printf '\033[1;32m✓\033[0m %s\n' "$*"; }
warn(){ printf '\033[1;33m!\033[0m %s\n' "$*"; }

kill_port() {
  local port="$1"
  local pids
  pids="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)"
  if [ -n "$pids" ]; then
    # shellcheck disable=SC2086
    kill $pids 2>/dev/null || true
    sleep 1
    pids="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)"
    if [ -n "$pids" ]; then
      # shellcheck disable=SC2086
      kill -9 $pids 2>/dev/null || true
      sleep 1
    fi
  fi
}

say "killing whatever is on :8080 and :5173"
kill_port 8080
kill_port 5173
# Defense in depth: catch alfred-server processes bound to other ports
# (a stray run during debugging) and any zombie vite by name match.
pkill -f "$SERVER_BIN" 2>/dev/null || true
pkill -f "bin/alfred-server" 2>/dev/null || true
pkill -f "node.*vite" 2>/dev/null || true
sleep 1
ok "ports clear"

if [ "$STOP_ONLY" -eq 1 ]; then
  ok "stopped (–stop requested; not restarting)"
  exit 0
fi

cd "$REPO_ROOT"

if [ "$SKIP_BUILD" -eq 0 ]; then
  say "building frontend (web/dist) and embedding into internal/static/dist"
  # Mirrors `make embed-web` but inlined so this script stays self-contained.
  (cd web && npm run build >/dev/null)
  mkdir -p internal/static/dist
  find internal/static/dist -mindepth 1 -delete
  cp -R web/dist/. internal/static/dist/
  ok "frontend rebuilt + embedded"
else
  warn "--no-build: skipping frontend rebuild; :8080 will serve the LAST embedded bundle"
fi

say "building alfred-server → $SERVER_BIN"
go build -o "$SERVER_BIN" ./cmd/alfred-server
ok "alfred-server built"

say "starting alfred-server (:8080) — log: $SERVER_LOG"
ALFRED_USER="$USER_NAME" \
ALFRED_PASSWORD="$PASSWORD" \
ALFRED_TOKEN="$TOKEN" \
ALFRED_DATA_DIR="$DATA_DIR" \
nohup "$SERVER_BIN" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
disown "$SERVER_PID" 2>/dev/null || true

say "starting vite dev (:5173) — log: $VITE_LOG"
(cd web && nohup npm run dev >"$VITE_LOG" 2>&1 &)
# Vite forks; we can't easily capture the child PID, but kill_port 5173
# handles it on the next run.

# Wait for both to answer 200. Poll up to ~15s each — Vite's first boot
# can take a few seconds, alfred-server usually <1s.
wait_ok() {
  local url="$1" name="$2"
  for _ in $(seq 1 30); do
    if curl -sf -o /dev/null "$url"; then
      ok "$name up: $url"
      return 0
    fi
    sleep 0.5
  done
  warn "$name did not respond at $url after 15s — check the log"
  return 1
}

wait_ok "http://localhost:8080/healthz" "alfred-server" || true
wait_ok "http://localhost:5173/"       "vite dev"      || true

cat <<EOF

  ┌─────────────────────────────────────────────────────────────
  │ Local dev is up. Same backend ($SERVER_BIN), two frontends:
  │
  │   http://localhost:5173/   ← Vite HMR (edit web/src/, see live)
  │   http://localhost:8080/   ← embedded prod bundle (the snapshot
  │                              just rebuilt; re-run this script
  │                              after any web/src change to refresh)
  │
  │   login: $USER_NAME / $PASSWORD
  │   data:  $DATA_DIR
  │   logs:  $SERVER_LOG  $VITE_LOG
  │
  │ To stop: ./scripts/local-dev.sh --stop
  └─────────────────────────────────────────────────────────────
EOF
