#!/usr/bin/env bash
# Find alfred sessions from RUNNING RESOURCES — no API call, no token,
# no server needed. Reads two OS-level sources of truth and shows them
# side by side:
#
#   1. sessions.json  → the AUTHORITATIVE mode (shell / claude). alfred
#                        keeps mode as logical state; tmux never stores it.
#   2. tmux socket     → the PHYSICAL pane: is it actually running bash,
#                        or a claude process, right now.
#
# These can DISAGREE — a session can be marked mode=claude before the
# claude process is actually spawned in the pane (or after it exits but
# before mode flips back). That gap is real; this script surfaces it in
# the PANE_PROC column so you can see logical-vs-physical at a glance.
#
# Usage:
#   ./scripts/find-sessions.sh                      # default data dir
#   ALFRED_DATA_DIR=/path/to/data ./scripts/find-sessions.sh
#
# Env:
#   ALFRED_DATA_DIR   (default: /tmp/alfred-local-data) — holds both
#                     sessions.json and alfred-tmux.sock.

set -euo pipefail

DATA_DIR="${ALFRED_DATA_DIR:-/tmp/alfred-local-data}"
SESSIONS_JSON="$DATA_DIR/sessions.json"
SOCK="$DATA_DIR/alfred-tmux.sock"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
dim()  { printf '\033[2m%s\033[0m\n' "$*"; }

bold "alfred sessions  (data dir: $DATA_DIR)"

if [ ! -f "$SESSIONS_JSON" ]; then
  echo "  no sessions.json at $SESSIONS_JSON — wrong data dir, or no sessions yet."
  exit 0
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "  jq not found — install it (brew install jq) or read $SESSIONS_JSON directly."
  exit 1
fi

# sessions.json is a single object when there's one session, an array
# when there are many. Normalise to an array so jq is uniform.
META="$(jq 'if type == "array" then . else [.] end' "$SESSIONS_JSON")"

# Snapshot tmux's session_name -> pane_current_command as plain TSV
# lines (no associative array — macOS ships bash 3.2 which lacks
# `declare -A`). We grep this snapshot per session below. Absent socket
# == no live panes (server down, or persisted-but-not-reconciled).
TMUX_UP=0
PANE_SNAP=""
if [ -S "$SOCK" ] && tmux -S "$SOCK" ls >/dev/null 2>&1; then
  TMUX_UP=1
  PANE_SNAP="$(tmux -S "$SOCK" list-panes -a \
                 -F '#{session_name}'$'\t''#{pane_current_command}' 2>/dev/null)"
fi

# pane_proc_for <session_id> → the pane's current command, or empty.
pane_proc_for() {
  printf '%s\n' "$PANE_SNAP" | awk -F'\t' -v s="$1" '$1==s {print $2; exit}'
}

# Emit one row per session. PANE_PROC reflects physical reality:
#   <bash/claude/…>  the pane's current command
#   (no pane)        session in metadata but no live tmux pane
#   (tmux down)      tmux server isn't running on the socket at all
printf '%s\t%s\t%s\t%s\t%s\n' "MODE" "PANE_PROC" "RENDERER" "ID" "NAME" > /tmp/.alfred-sessions.$$
while IFS=$'\t' read -r mode renderer id name; do
  if [ "$TMUX_UP" -eq 0 ]; then
    proc="(tmux down)"
  else
    proc="$(pane_proc_for "$id")"
    [ -n "$proc" ] || proc="(no pane)"
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' "$mode" "$proc" "$renderer" "$id" "$name" \
    >> /tmp/.alfred-sessions.$$
done < <(echo "$META" | jq -r '.[] | [(.mode//"shell"), (.renderer//"-"), .id, .name] | @tsv')

column -t -s $'\t' /tmp/.alfred-sessions.$$
rm -f /tmp/.alfred-sessions.$$

# Footer: flag the logical-vs-physical mismatch explicitly, since it's
# the whole reason this script reads BOTH sources instead of just one.
if [ "$TMUX_UP" -eq 1 ]; then
  dim "PANE_PROC is the live tmux pane process; MODE is alfred's logical state — they can differ."
else
  dim "tmux server not running on $SOCK — MODE shown from sessions.json only."
fi
