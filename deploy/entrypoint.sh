#!/bin/bash
# Run alfred-server in a respawn loop so the tmux server (which alfred
# starts as a daemon and is reparented to PID 1 = tini) survives across
# alfred-server crashes/restarts. This is what makes "Go-restart sessions
# survive" actually work in a container: the alfred process can die and
# come back, while in-flight bash subprocesses keep running inside the
# still-alive tmux server.
set -u

while true; do
  /usr/local/bin/alfred-server || true
  # Brief delay so a tight crash loop doesn't peg CPU.
  sleep 0.5
done
