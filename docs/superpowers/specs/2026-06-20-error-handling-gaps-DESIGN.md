# Error-Handling Gaps — Feature Factory DESIGN

**Date:** 2026-06-20
**Branch:** `next`
**Status:** Phase 1 complete — ready for hands-off build
**Source audit:** two parallel code sweeps (backend Apply layer + frontend reducer) cataloged ~20 error cases; 7 confirmed real GAPs, each pinned by a failing test in `internal/claudestate/error_cases_test.go` and `web/src/features/sessions/errorCases.test.ts`.

## Context

A Claude UI turn/tool can terminate via ~12 paths. Audit found 7 cases where the reaction is wrong, inconsistent front↔back, or leaves state stuck. Each GAP already has a **failing test** that documents the correct behavior — the build turns those red tests green.

This DESIGN folds the GAP fixes together with the previously-specced **outcome semantics refactor** (`2026-06-20-turn-tool-outcome-semantics-design.md`), because GAP-5 (unconditional `isError`) is exactly what the outcome enum fixes.

## ADR Ledger

- **ADR-001: Malformed terminator payloads must still finalize the turn.** When `Apply` gets a wrong-type payload for `EventResult` / `EventClaudeRunEnded` / `EventClaudeError`, it currently `return`s an error and skips the finalize, stranding the turn `Done=false / InFlight=true` forever. Fix: on a bad terminator payload, still run a synthetic finalize (mark the turn aborted + clear InFlight) before returning the error, so the composer always unlocks. — *Rationale: a terminator that fails to terminate is the worst failure mode (permanent spinner). Fail safe = finalize.* — pins GAP 1-3.

- **ADR-002: A dropped `tool_result` must be observable.** A `tool_result` for a `ToolUseID` with no matching block is silently discarded (no log, no error). Fix: emit a `slog.Warn("tool_result for unknown toolUseId", ...)` so the drop is greppable. — *Rationale: silent data loss is undebuggable; this is a one-line observability fix, not a behavior change.* — pins GAP-4.

- **ADR-003: Turn/tool terminal state is an explicit `outcome` enum.** Adopt the outcome model (`completed`/`errored`/`aborted`, tool also `denied`) from the outcome-semantics spec. `done`/`isError` become derived. `finalizeInFlight` / `finalizeInFlightTurn` set `aborted` (not unconditional `errored`). — *Rationale: distinguishes external interrupt from Claude's own error; fixes the "Done" lie.* — supersedes the unconditional-isError behavior. Pins GAP-5.

- **ADR-004: Unmatched `turn_started` adopts the orphan placeholder.** When `turn_started` arrives with a `clientNonce` matching no turn, the reducer still re-keys the lone `pending:<nonce>` placeholder to the server's `turnId` (server is source of truth). If there is no pending placeholder at all, append a turn under the server `turnId`. — *Rationale: never leave a `pending:` orphan stranded; the server's id wins.* — pins GAP-6.

- **ADR-005: `lastError` clears on the next successful result AND stays manually dismissible.** A successful `result` (isError=false) clears `lastError` even when no fresh `beginClaudeTurn` ran (server-driven / reattach path). The existing manual `clearError` dismiss button is kept and verified to actually clear state. — *Rationale: a stale red banner after recovery is misleading; both auto-clear and manual dismiss serve different recovery shapes.* — pins GAP-7.

## Open Questions

- **OQ-01:** Should the backend ever *populate* `Pending[]`/`PendingQuestions[]` (audit case 6.1/7.1 — they are only ever cleared, never filled)? **Deferred** — out of scope for this build; the approval flow currently works via direct WS frames. Flag for a future design. Build agents: *do not touch the Pending population path.*

## Task DAG

Global: `TEST_CMD_GO = go test ./internal/claudestate/`, `TEST_CMD_WEB = cd web && npx vitest run`, `LINT_GO = go vet ./...`, `LINT_WEB = cd web && npx tsc --noEmit`.

Each task turns specific red tests green. Tester runs the named tests and asserts they PASS (and the full suite stays green).

| Task | Title | depends_on | ADR | Acceptance gate |
|---|---|---|---|---|
| **T1** | Backend: bad terminator payload → synthetic finalize | — | ADR-001 | `go test ./internal/claudestate/ -run 'TestApply_ResultBadPayload\|TestApply_RunEndedBadPayload\|TestApply_ClaudeErrorBadPayload'` all PASS; full `go test ./internal/claudestate/` green |
| **T2** | Backend: WARN on tool_result with no matching block | — | ADR-002 | `go test ./internal/claudestate/ -run 'TestApply_ToolResult_NoMatchingBlock'` PASS; assert a log line is emitted (capture slog) |
| **T3** | Outcome enum + setTurnOutcome/setToolOutcome + 12-path wiring | — | ADR-003 | Implement per `2026-06-20-turn-tool-outcome-semantics.md` plan Tasks 1-7; `go test ./internal/claudestate/` + `cd web && npx vitest run` green; the outcome-mislabel GAP test (`finalizeInFlightTurn ... aborted`) PASS |
| **T4** | Frontend: unmatched turn_started adopts orphan | T3 | ADR-004 | `cd web && npx vitest run errorCases` — the unmatched-nonce GAP test PASS; full vitest green |
| **T5** | Frontend: lastError clears on success + manual dismiss verified | T3 | ADR-005 | `cd web && npx vitest run errorCases` — the lastError-persist GAP test PASS; add a dismiss-clears test; full vitest green |
| **T6** | Verify all 7 GAP tests green + suites green + interrupt log fires | T1,T2,T3,T4,T5 | all | `go test ./internal/claudestate/ && cd web && npx vitest run` both fully green; `grep "terminated abnormally" /tmp/alfred-server.log` shows a reason-tagged line after a restart |

Notes:
- T1, T2, T3 are independent (different files / concerns) → can run concurrently.
- T4, T5 depend on T3 (they rely on the TS `outcome` field landing first).
- T6 is the integration gate — runs last, depends on all.

## Build discipline (baked into agent prompts)
- Additive / backward-compatible; no snapshot version bump (outcome fields are `omitempty` + backfilled).
- Each delivery reports its PR head commit; each merge reports the trunk hash.
- Cite the ADR; respect OQ-01 (do not touch Pending population).
- Rework = fixup commits on the same PR branch, never reset/rewrite.
- The red audit tests in `error_cases_test.go` / `errorCases.test.ts` are the regression net — they must end green, never deleted.
