package claudestate

import (
	"log/slog"
	"time"
)

// isErrorOutcome reports whether an outcome should render as an error
// (red). Both Claude's own errors and external interrupts do.
func isErrorOutcome(outcome string) bool {
	return outcome == "errored" || outcome == "aborted"
}

// setTurnOutcome is the SOLE writer of a turn's terminal state.
// Done/IsError/FinishedAt are derived from outcome so the 12 termination
// paths can never disagree. First terminator wins (an already-terminated
// turn is left untouched), preserving the prior turn.Done guard.
func setTurnOutcome(t *ClaudeTurn, outcome, reason string, ts time.Time) {
	if t == nil || t.Outcome != "" {
		return
	}
	t.Outcome = outcome
	t.AbortReason = reason
	t.Done = true
	t.IsError = isErrorOutcome(outcome)
	t.FinishedAt = timePtr(ts)
}

// setToolOutcome is the sole writer of a tool block's terminal state.
// "denied" is a terminal state but not an error.
func setToolOutcome(t *ClaudeToolCall, outcome string, ts time.Time) {
	if t == nil || t.Outcome != "" {
		return
	}
	t.Outcome = outcome
	t.IsError = isErrorOutcome(outcome)
	if t.FinishedAt == nil {
		t.FinishedAt = timePtr(ts)
	}
}

// backfillOutcome derives Outcome for a pre-outcome snapshot turn. Old
// snapshots can't tell errored from aborted (history is gone), so a done
// turn backfills to errored/completed via IsError — both render red, the
// conservative choice. Only newly-produced interrupts get "aborted".
func backfillOutcome(t *ClaudeTurn) {
	if t == nil || t.Outcome != "" || !t.Done {
		return
	}
	if t.IsError {
		t.Outcome = "errored"
	} else {
		t.Outcome = "completed"
	}
}

// classifyClaudeError maps a ClaudeError code to (abortReason, outcome).
// Server-shutdown is an external interrupt (aborted); spawn/other CLI
// failures are the CLI's own errors (errored).
func classifyClaudeError(code string) (reason, outcome string) {
	switch code {
	case "server_shutdown":
		return "server_shutdown", "aborted"
	case "claude_spawn_failed":
		return "spawn_failed", "errored"
	default:
		return code, "errored"
	}
}

// logAbnormalTermination emits the one greppable interrupt log used by
// every errored/aborted path. hangingTools is the count of tool blocks
// the same finalize closed (0 for the common case).
func logAbnormalTermination(sessionID, turnID, outcome, reason string, hangingTools int) {
	slog.Warn("claude turn terminated abnormally",
		"sessionId", sessionID,
		"turnId", turnID,
		"outcome", outcome,
		"reason", reason,
		"hangingTools", hangingTools)
}
