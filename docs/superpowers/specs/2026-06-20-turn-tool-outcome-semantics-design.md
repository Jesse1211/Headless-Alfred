# Turn/Tool Outcome Semantics — Design

**Date:** 2026-06-20
**Status:** Approved, ready for implementation plan
**Branch:** `next`

## Problem

A Claude UI turn (and each tool inside it) has **three** real end states, but
the code encodes them in **two** booleans (`done` + `isError`). The third state
collides with the second, so the frontend and backend cannot tell them apart.

| Real outcome | Meaning | Current encoding | Problem |
|---|---|---|---|
| Claude answered normally | **completed** | `done:true, isError:false` | ✅ fine |
| Claude itself errored (rate limit, tool failure, model error) | **errored** | `done:true, isError:true` | ⚠️ collides ↓ |
| Killed by something external (SIGKILL, restart, WS drop, shutdown, stuck) | **aborted** | `done:true, isError:true` | ❌ **indistinguishable from errored** |

### Concrete symptoms found in the audit

The backend has **12 termination paths**; the frontend consumes `done`/`isError`/
`decision`. The audit (two parallel code sweeps) found:

1. **`turnPhase()` (`ClaudeChatView.tsx:347`) `if (turn.done) return 'Done'`** — never
   checks `isError`. An aborted/errored turn shows a green "Done" chip. This is the
   "完成了" lie the user reported on a runner-killed Edit.
2. **`finalizeInFlightTurn` / all backstop paths set `isError:true` unconditionally** —
   even a clean interrupt is mislabeled an error. No way to say "interrupted, not errored".
3. **Hanging tool blocks are marked `decision:"deny"`** — but `deny` means *the user
   rejected it*. A killed runner ≠ a user deny. The frontend reverse-engineers "interrupted"
   from `result.startsWith("Interrupted")` — a brittle string-prefix match that breaks if the
   message text changes.
4. **The error banner does not distinguish error codes** — `server_shutdown`,
   `claude_spawn_failed`, `busy`, `history_unavailable` all render as one line of text.
5. **A `done` tool can still carry `isError`** — green "done" + red error text overlaid,
   ambiguous whether it succeeded.
6. **Interrupts have no unified, greppable log** — only the load-time hanging-tool fixup logs.
   WS-disconnect / shutdown / reaper paths log inconsistently or not at all, so you cannot
   grep "how many times did this session get interrupted, and why".

## Solution: an explicit `outcome` enum as the source of truth

Add one field to `ClaudeTurn` and `ClaudeToolCall`:

```go
// Outcome is the terminal state. "" == still in progress (not terminated).
// Replaces reverse-engineering the end state from done+isError.
Outcome string `json:"outcome,omitempty"` // "" | "completed" | "errored" | "aborted"
                                           // (tool also: "denied")
```

`done` and `isError` are **kept as derived quantities** (not deleted), so the large set of
existing consumers (the `is-error` CSS, `computeInFlight`) need no change:

- `done   = (outcome != "")`
- `isError = (outcome == "errored" || outcome == "aborted")`

Only `turnPhase()` / `toolStatus()` switch to reading `outcome` directly, which lets them
say "Interrupted" instead of lying with "Done", and drops the brittle string match.

### Why keep done/isError instead of replacing them

12 backend paths + many frontend consumers use them. Keeping them as derived quantities makes
`outcome` an **additive** source of truth: small migration surface, low risk. `omitempty` +
load-time backfill means old snapshots need no version bump.

## Unified write path

A single helper is the **only** writer of a turn's terminal state, eliminating the
"12 paths each hand-write fields" drift:

```go
// setTurnOutcome is the sole entry point for a turn's terminal state.
// done/isError/finishedAt are all derived. First terminator wins
// (preserves the existing turn.Done guard semantics).
func setTurnOutcome(t *ClaudeTurn, outcome string, ts time.Time) {
    if t.Outcome != "" { return }
    t.Outcome = outcome
    t.Done = true
    t.IsError = (outcome == "errored" || outcome == "aborted")
    t.FinishedAt = &ts
}
```

`setToolOutcome` is analogous (adds `denied` for user rejection, which is a legitimate
terminal state distinct from `aborted`).

### Termination path → outcome mapping

The dividing line: **Claude/CLI's own failure = `errored`; external interruption = `aborted`.**

| Path | Trigger | outcome |
|---|---|---|
| `applyResult` (is_error=false) | normal result | **completed** |
| `applyResult` (is_error=true) | Claude error result | **errored** |
| spawn failure | binary missing, perms | **errored** |
| rate_limit fatal | limit termination | **errored** |
| runner death (reaper) | process exited unexpectedly | **aborted** |
| WS disconnect | client dropped | **aborted** |
| graceful shutdown | SIGTERM | **aborted** |
| stale trailing turn (Load) | server restart | **aborted** |
| hanging tool (Load) | stuck tool | tool → **aborted** |
| user deny | tool rejected | tool → **denied** |
| force-kill bg task (Load) | restart | (bg already has 5 statuses — unchanged) |

## Structured interrupt logging + reason

Every aborted/errored path records a **machine-readable reason code** on the turn AND emits a
**unified, greppable** log line; the existing human-readable synthetic text block is kept for
the user.

```go
// Machine-readable, on the turn (non-empty only for errored/aborted):
AbortReason string `json:"abortReason,omitempty"`
// e.g. "runner_killed" | "ws_disconnect" | "server_shutdown" | "server_restart" |
//      "spawn_failed" | "rate_limit"

// Unified log, same msg+fields across all 12 paths:
slog.Warn("claude turn terminated abnormally",
    "sessionId", sid, "turnId", t.ID,
    "outcome", outcome,    // errored | aborted
    "reason", reason,      // runner_killed | ws_disconnect | ...
    "hangingTools", n)
```

Operators run `grep "terminated abnormally" | jq '.reason'` to list every interrupt with its
cause — directly answering "it got interrupted several times, what happened each time".

## Frontend rendering

`outcome` + `abortReason` drive honest rendering:

- **`turnPhase()`** switches on `outcome`: `completed → "Done"` (green), `errored → "Error"`
  (red), `aborted → "Interrupted"` (orange). Falls through to the existing
  Initializing/Calling X/Thinking logic while not terminated.
- **`toolStatus()`** switches on `outcome`: `aborted → "interrupted"`, `errored → "errored"`,
  `completed → "completed"`, `denied → "denied"`. Drops the `result.startsWith("Interrupted")`
  hack.
- **Error banner**: aborted and errored both render as a **red error banner** (simple,
  consistent). The phase chip's "Interrupted" vs "Error" text preserves the semantic
  distinction, so no information is lost.
- **Timers**: `finishedAt` is set via `setTurnOutcome`, so the runaway elapsed timer stops
  (mechanism already verified).
- **`inFlight` stays `outcome == ""`** — all three terminal states unlock the composer.
  **Activity (can I send next?) and outcome (how did it end?) are fully decoupled** — the
  original concern.

## Migration & compatibility

Old snapshots have no `outcome`. Backfill once on `Load` (no version bump — purely additive):

```go
func backfillOutcome(t *ClaudeTurn) {
    if t.Outcome != "" || !t.Done { return }
    if t.IsError { t.Outcome = "errored" } else { t.Outcome = "completed" }
}
```

Old snapshots cannot distinguish errored vs aborted (the history is gone); backfilling to
`errored` is the safe conservative choice (both render red). Only newly-produced interrupts get
the correct `aborted`.

## Testing (TDD, red-before-green per case)

| Layer | Tests |
|---|---|
| Go: setTurnOutcome | first terminator wins; completed/errored/aborted derive done/isError correctly |
| Go: path mapping | reaper→aborted, spawn→errored, result(is_error)→errored, result→completed, shutdown→aborted |
| Go: load migration | old snapshot (no outcome) backfills; hanging tool→aborted; stale turn→aborted |
| Go: logging | termination emits `terminated abnormally` + reason field (assert via captured slog handler) |
| Frontend: turnPhase | three outcomes → Done/Error/Interrupted |
| Frontend: toolStatus | aborted→interrupted (no string-prefix); denied vs interrupted distinguished |

## Implementation phases (for review + rollback)

1. Go data model + `setTurnOutcome`/`setToolOutcome` helper + tests
2. Wire the 12 paths through the helper + unified logging + tests
3. Load migration/fixup through the helper + tests
4. TS type mirror + frontend `turnPhase`/`toolStatus`/banner + tests

## Non-goals

- No change to BgTask's existing 5-status enum (`in_progress`/`completed`/`failed`/`killed`/
  `stopped`) — it already distinguishes its terminal states.
- No snapshot version bump (the new fields are additive + backfilled).
- No new UI for per-reason banner styling — aborted and errored share the red banner; the
  phase chip carries the distinction.
