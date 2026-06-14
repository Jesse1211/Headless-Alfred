#!/bin/sh
# alfred-claude-bridge — the PreToolUse hook that ships in the
# runtime image and is configured in ~/.claude/settings.json.
#
# Contract:
#   stdin  = the hook payload (a JSON object from claude)
#   stdout = the decision (a JSON object claude expects)
#   exit   = 0 on success, non-zero on error (claude treats as deny)
#
# Implementation: forward stdin to the bridge HTTP endpoint and
# write the HTTP response body to stdout. The bridge blocks until
# the user (via the React UI) makes a decision, so this curl can
# legitimately take minutes; we set a 10-minute timeout to bound
# it in case the bridge itself is gone.
#
# The bridge port is fixed at 8090 (see internal/claude/bridge.go).

set -e

curl --silent --show-error \
     --max-time 600 \
     --header 'Content-Type: application/json' \
     --data-binary @- \
     http://127.0.0.1:8090/tool-approval
