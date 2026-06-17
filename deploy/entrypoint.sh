#!/bin/bash
# Run alfred-server in a respawn loop so the tmux server (which alfred
# starts as a daemon and is reparented to PID 1 = tini) survives across
# alfred-server crashes/restarts. This is what makes "Go-restart sessions
# survive" actually work in a container: the alfred process can die and
# come back, while in-flight bash subprocesses keep running inside the
# still-alive tmux server.
set -u

# Make sure ~/.claude is owner-writable. When the PVC subPath is
# freshly created, kubelet leaves it mode 0755 owned by our fsGroup;
# the Claude CLI then can't drop new files into it. mkdir -p + chmod
# is idempotent and only adjusts perms we already have permission to
# change (we run as UID 1000 = the dir's owner after fsGroup fix).
mkdir -p "$HOME/.claude" 2>/dev/null || true
chmod 0700 "$HOME/.claude" 2>/dev/null || true

while true; do
  /usr/local/bin/alfred-server || true
  # Brief delay so a tight crash loop doesn't peg CPU.
  sleep 0.5
done
