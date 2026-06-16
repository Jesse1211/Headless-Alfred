# Code Simplification Audit — 2026-06-16

Scope: backend `internal/` + `cmd/alfred-server/`, frontend `web/src/`.
Branch: `main` (working snapshot).

This is a survey. No code was changed.

The audit is biased: every recommendation answers "does the next feature get
visibly easier or safer?" — not "does this look cleaner?" Where a parallel
exists but unifying it would hurt, that's called out in `What NOT to refactor`.

---

## Highest-leverage simplifications (rank-ordered by impact / cost ratio)

### 1. Extract `useDocumentSync(sessionID, counter, fetcher)` for the three right-rail data layers

**What**: The "fetch once, refetch silently on WS counter bump, swallow alive flag, render loading/error/empty" pattern is implemented three times with subtle drift:
- `web/src/features/sessions/SummarySidebar.tsx:22-49` — keyed on `sessionID` + `summaryFetchCounter`, with a `firstSummaryFetchDone` ref so only the first fetch shows a spinner.
- `web/src/features/sessions/RecapSidebar.tsx:36-83` — two near-identical effects (date list + selected-date content), both keyed on `recapFetchCounter`, with their own `firstListFetchDone` ref and 404-to-empty-string special case.
- `web/src/features/sessions/NotesPanel.tsx:33-59` — same shape, plus a `document.activeElement` focus guard that prevents the server echo from clobbering in-flight typing.

The shapes of the three "loaded/loading/error" tuples differ enough that the existing duplication is bug-prone: `NotesPanel` uses `loaded` (a state), `SummarySection` uses `firstSummaryFetchDone` (a ref + a separate `summaryLoading` state), `RecapSidebar` uses `firstListFetchDone`. The "skip-when-typing" guard exists only in `NotesPanel`. Future panels (e.g. a per-session token-usage budget panel) will copy whichever one was nearest.

**Why it's worth it**: ~50 LOC of state plumbing collapse into one call site each. The three sidebars become declarative. The 404-to-empty-string behaviour stops being implicit in `RecapSidebar` (currently buried in a `.catch`). Most importantly, the next right-rail panel ships in ~30 LOC instead of ~80.

**Risk**: Low. The `NotesPanel` focus guard must be explicitly composable (an optional `skipRefetchIf: () => boolean` argument), otherwise we silently drop a real bug fix from CONTEXT.md's "Notes panel must not be clobbered by server echo" trap (CONTEXT.md doesn't explicitly catalog this one, but `NotesPanel.tsx:38-44`'s comment does). The debounced *save* in `NotesPanel` stays where it is — it's a different concern.

**Suggested approach** (signature, not implementation):
```ts
function useDocumentSync<T>(args: {
  key: string                       // sessionID or date — triggers reset
  counter: number                   // WS-bumped refetch trigger
  fetcher: (key: string) => Promise<T>
  initial: T                        // for the empty render branch
  skipRefetchIf?: () => boolean     // notes: don't clobber typing
  notFoundIsEmpty?: boolean         // recap content: 404 → initial
}): { value: T; loading: boolean; error: string | null }
```
Call sites become 3–4 lines each.

**LOC delta**: −60 in the three sidebars, +35 in a new `useDocumentSync.ts` ⇒ **net −25**, but each future panel saves another ~30.

**Who calls this differently**: only the three named components. NotesPanel's debounced save stays.

---

### 2. Extract `internal/diskwatcher` to collapse the three fsnotify watchers

**What**: `internal/{summary,notes,recap}/watcher.go` are three implementations of "directory watcher with per-key debounce and graceful drain":
- `internal/summary/watcher.go:38-146` (146 LOC)
- `internal/notes/watcher.go:29-124` (124 LOC)
- `internal/recap/watcher.go:29-117` (117 LOC)

The diff between them is genuinely tiny:
- Filename parser: `<sid>.md` (summary, notes) vs `<YYYY-MM-DD>.md` (recap).
- Callback key type: `string` in all three (sid or date).
- Log message prefix.
- Minor ordering difference: `recap.Stop` does `close(w.stop) → w.Close() → <-done`, the other two do `close → <-done → w.Close()`. There's no documented reason for this drift — looks accidental.

The CONTEXT.md `Quick orientation` entries say the same thing for all three ("watcher in internal/X/watcher.go emits Y_updated WS frames"). The three packages were built in three separate plans (summary in 2026-06-15, notes shortly after, recap on the same day as summary-template) and the third was never folded into the first.

**Why it's worth it**:
- Net −230 LOC (3 × ~120 → 1 × ~120 + 3 × ~15-line adapter).
- One place to fix the next race (the Stop-order drift between recap and the other two is exactly the kind of latent bug this prevents).
- The fourth file-per-X feature (e.g. per-session pinned prompts, per-session token budgets, per-session model preference) ships with a 5-line filename parser, not a copied watcher.

**Risk**: Low. The watcher behaviour is well-tested per package (`summary/watcher_test.go` exists). Generic over the key type via Go generics (`type Watcher[K comparable]`) keeps callsites typed. The Stop-order question must be picked deliberately and noted — recommend the `close(stop) → <-done → close(fsnotify)` order from summary/notes (drain pending in-flight events before tearing down the source).

**Suggested approach**:
```go
// internal/diskwatcher/watcher.go
package diskwatcher

type Watcher[K comparable] struct { /* ... */ }

func Start[K comparable](
    dir string,
    parse func(filename string) (K, bool),
    debounce time.Duration,
    onWrite func(K),
) (*Watcher[K], error)

func (w *Watcher[K]) Stop()
```
The three call sites in `cmd/alfred-server/main.go` and `internal/api/ws.go` become:
```go
sw, err := diskwatcher.Start(summary.Dir(dataDir), summary.ParseFilename, 200*time.Millisecond, onWrite)
```
Each domain package keeps its `Path/Dir/ParseFilename` helpers (4–10 LOC each); the 100-LOC watcher implementation is gone.

**LOC delta**: −230 net (−380 from the three watchers, +150 new generic + adapter helpers in each domain package).

**Who calls this differently**: `cmd/alfred-server/main.go:155` (recap), `internal/api/ws.go:189` (summary), `internal/api/ws.go:202` (notes). The frontend is untouched.

---

### 3. Extract `safeReadMarkdownFile(root, basename)` for the three GET-by-id handlers

**What**: Three handlers implement the same path-traversal + 404-on-missing + `text/markdown` write pattern:
- `internal/api/summary_handler.go:23-53` — uses `summary.Dir`, sid-shaped basename.
- `internal/api/notes_handler.go:23-49` — uses `notes.Dir`, sid-shaped basename.
- `internal/api/recap_handlers.go:68-94` — uses `recap.Dir`, date-shaped basename.

The shared logic: validate basename, `filepath.Clean`, prefix-check against root, `os.ReadFile`, map `os.ErrNotExist → 404`, set `Content-Type: text/markdown; charset=utf-8`, write body. The validation differs in form but not substance — sid validation is "no slashes, no `..`"; date validation is a regex match. Both are guards that reject before the file open.

**Why it's worth it**:
- ~60 LOC removed from `api/`.
- The hardening (the prefix check on the *resolved* path, not the input) becomes a single place to audit during security review.
- Adding the next file-per-X handler shrinks from ~30 LOC to ~10.
- Future enhancement (e.g. `If-None-Match` / ETag for cacheable responses, or last-modified) only has to land once.

**Risk**: Very low. The three handlers are structurally identical, the differences (return code on traversal — 404 in all three) already match. The validators stay per-handler since they encode the domain shape (sid vs date).

**Suggested approach**:
```go
// internal/api/markdownfs.go
func serveMarkdownFile(w http.ResponseWriter, root, basename string) {
    // assumes basename is already validated by caller
    clean := filepath.Clean(filepath.Join(root, basename))
    if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
        writeError(w, http.StatusNotFound, "not_found", "no such file")
        return
    }
    body, err := os.ReadFile(clean)
    if errors.Is(err, os.ErrNotExist) {
        writeError(w, http.StatusNotFound, "not_found", "no such file")
        return
    }
    if err != nil {
        writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
        return
    }
    w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
    _, _ = w.Write(body)
}
```
Each handler becomes ~10 lines: pull sid/date, validate shape, call `serveMarkdownFile(w, root, basename+".md")`. The PUT path in notes stays separate (it has the atomic-write + size-cap logic which is its own concern).

**LOC delta**: −60 net (−110 from three handlers, +30 in helper, +20 unchanged validators).

**Who calls this differently**: Only `summary_handler.go`, `notes_handler.go`, and `recap_handlers.go::GetRecapHandler`.

---

### 4. Extract `isCanonicalIO(req, dir, basenamePattern, maxBytes)` for the dispatcher auto-allow predicates

**What**: `internal/claude/dispatcher_summary.go:24-52` and `internal/claude/dispatcher_recap.go:24-59` are the same shape with three knobs:
- The target dir (computed from `summary.Dir(dataDir, sid)` vs `recap.Dir(dataDir)`).
- The basename pattern (exact match on `<sid>.md` vs regex `^[0-9]{4}-[0-9]{2}-[0-9]{2}\.md$`).
- The size cap (8KB vs 16KB).

Both call sites are inside `Dispatcher.OnAsk` (`internal/claude/dispatcher.go:120-127`), six lines apart. When the next auto-allow target arrives (likely candidate: per-session pinned scratchpad, project-scoped settings file), the pattern gets copied a third time.

**Why it's worth it**:
- ~50 LOC removed.
- The `Read+Write+size-cap+json-decode` pattern only has to be reasoned about once for security review (this is the auto-bypass-permissions surface — getting it wrong means Claude can silently write arbitrary files).
- Adding a third file-class (per-session pinned notes that go INTO Claude's prompt, for instance) goes from ~50 LOC to ~10.

**Risk**: Medium. This is a *security* boundary — every byte of these predicates was deliberately written. The summary predicate uses **exact string equality** on file_path (no normalization), whereas the recap predicate uses `filepath.Clean` + parent-dir check + basename regex. Unifying them must NOT loosen the summary predicate (currently the tightest). Recommend the unified helper take a `match func(path string) bool` so each call site keeps its own match policy:

```go
// internal/claude/canonical_io.go
func isCanonicalIO(req PendingRequest, maxBytes int, matchPath func(string) bool) bool {
    var in struct {
        FilePath string `json:"file_path"`
        Content  string `json:"content"`  // only inspected for Write
    }
    if err := json.Unmarshal(req.ToolInput, &in); err != nil {
        return false
    }
    if !matchPath(in.FilePath) {
        return false
    }
    switch req.ToolName {
    case "Read":
        return true
    case "Write":
        return len(in.Content) <= maxBytes
    }
    return false
}
```
Then `isSummaryIO` is 3 lines (closure over `wantPath` for exact match), `isRecapIO` is 5 lines (closure over the dir + regex). Total dispatcher_*.go drops to ~20 LOC each, and the read+write+unmarshal+JSON-tag boilerplate lives in one place.

**LOC delta**: −40 net (−90 from the two predicate files, +50 in the helper).

**Who calls this differently**: only `Dispatcher.OnAsk` in `internal/claude/dispatcher.go:120-127` (no behaviour change). Tests in `internal/claude/` that exercise the predicates need to be re-pointed but the assertions stay identical.

---

### 5. Split `useSessions` into thematic hooks composed by `WorkspacePage`

**What**: `web/src/features/sessions/useSessions.ts` is 407 LOC and returns 22 named values. It owns:
- WS connection state machine (`connState`, the `ShellSocket` instance, the `onMessage` dispatcher).
- Sessions list + selection + per-session state Map.
- Claude lifecycle (`enterClaude`, `exitClaude`, `claudePrompt`, `toolDecision`, `submitQuestionAnswer`, `interruptClaude`).
- Stdin/PTY pump for TUI mode (`registerPtyHandler`, `sendStdin`).
- Recap special-cases (`recapFetchCounter`, `createOrEnterRecap`, `setSessionMeta`).
- Errors (`lastError`, `clearError`).
- LocalStorage selection persistence.

`WorkspacePage.tsx` is 427 LOC and destructures 18 of these. Adding the next per-session feature (e.g. a token-budget counter that lives at the top level like `recapFetchCounter`) means editing `useSessions` (state + reducer case + return), `WorkspacePage` (destructure + thread into right rail), and at least one component.

`useSessions` is the project's only god-hook. CONTEXT.md `Quick orientation` already routes Claude UI / chat history / recap features back to it, so the centralization is by design — but the size has crossed the threshold where a new contributor needs to scroll to understand any one slice.

**Why it's worth it**:
- Each thematic hook becomes individually testable (currently the `useSessions.test.ts` is 225 LOC and only covers a fraction).
- Splitting into 3–4 thematic hooks lets the next feature slot in next to its peers instead of accreting into the trunk.
- `WorkspacePage` props drilling shrinks because callers reach for `useClaudeSession()` directly instead of pulling 6 fields from one giant hook.

**Risk**: Medium-high if done as a single mechanical split. The `onMessage` dispatcher *needs* to see frames from all domains (recap_updated goes to a top-level counter, claude_event goes into per-session claude state, summary_updated goes into a per-session counter). Splitting hooks naively means each hook would need its own `socket.onMessage` and they'd race or duplicate. The correct factoring keeps `useSessionsCore` owning the socket + base `perSession`/`sessions` state and event dispatch, with sub-hooks consuming those (`useClaudeSession(core, sid)`, `useRecapSession(core)`).

**Suggested approach**:
```ts
function useSessionsCore(token: string): {
  connState, sessions, perSession, selectedSessionID,
  selectSession, createSession, renameSession, closeSession,
  socket, lastError, clearError,
}

function useClaudeSession(core, sid): {
  state: ClaudeState | null
  enter, exit, prompt, decideTool, answerQuestion, interrupt
}

function useRecapSession(core): {
  fetchCounter, createOrEnter
}

function usePtyPump(core): {
  registerHandler, sendStdin
}
```
`WorkspacePage` composes the four; the prop sprawl into `RecapSidebar` / `ClaudeChatView` / `ClaudeTerminal` becomes a single hook call per component (or per concern). The reducer files stay as they are — they're already split (`sessionsReducer.ts` + `claudeReducer.ts`).

**LOC delta**: −0 net immediately (the code moves rather than shrinking), but each subsequent per-session feature gets a focused ~50-LOC hook instead of a ~100-LOC delta spanning two files. The maintainability win is the entire payoff.

**Who calls this differently**: `WorkspacePage.tsx` (every consumer of `useSessions`). Tests in `useSessions.test.ts` re-target; new per-hook tests become straightforward to write.

**Note**: I'd defer this until after #1 + #6 land — both shrink `WorkspacePage`'s right-rail prop sprawl independently, making the eventual hook split easier to scope.

---

### 6. Backend `mutateAndPersist` is already extracted — finish the job

**What**: `internal/session/manager.go:602-626` defines `mutateAndPersist(sessionID, mut)` and explicitly notes "could replace the body of SetMode too, but we leave SetMode alone to minimize this diff." Three methods still hand-roll the pattern that `mutateAndPersist` exists to absorb:
- `Manager.Rename` (`manager.go:391-420`) — 30 LOC, uses the same lock + snapshot + persist + fire-listener shape, plus a `m.onRename.Fire(...)`.
- `Manager.SetMode` (`manager.go:437-458`) — 22 LOC, identical shape minus the listener.
- The persistMetas-then-snapshot dance lives inside `Create` (`manager.go:217-247`) and `Close` (`manager.go:809-835`); those are more elaborate (rollback paths) so they stay separate.

`SetRenderer`, `SetTemplateID`, `SetClaudeBypass`, `EnsureClaudeConvoID` all already use `mutateAndPersist`. The unfinished commit history is visible in the comment on line 605.

**Why it's worth it**:
- −30 LOC.
- Closes the "should I copy the pattern or use the helper?" decision for the next contributor — currently the existing files give both answers.
- The "snapshot under lock, persist outside" invariant (which Rename's comment explains was a bug fix) becomes guaranteed for all mutations.

**Risk**: Very low for SetMode (a straight refactor). For Rename, the helper needs an optional post-persist hook so `m.onRename.Fire(...)` still runs:
```go
func (m *Manager) mutateAndPersist(sessionID string, mut func(*store.SessionMeta), after ...func()) error
```
Or simpler: keep `Rename` as a thin wrapper that calls `mutateAndPersist` then fires the listener.

**LOC delta**: −30 net.

**Who calls this differently**: Internal to `session.Manager`; no external API change. The renameEvent listener path is unaffected.

---

## Architectural observations (no immediate action needed)

### Type duplication between Go and TS is acceptable for now

Every WS frame is defined in `internal/api/wsproto.go` (Go) and `web/src/lib/ws.ts` (TS), and every REST shape (`SessionMeta`, `recapEntry`, command record) is defined twice. This is real duplication — adding a field requires two synced edits — but:
- The protocol surface is small enough (one `OutMsg` struct, one `InMsg` struct, plus ~8 REST shapes) that hand-sync is realistic.
- Codegen (gRPC, OpenAPI, TypeBox, ts-json-schema-generator from Go AST) would require setting up tooling, CI gating, and a generated-output convention. For a single-developer personal tool the cost is way above the benefit.
- The TS side is `ServerMsg` as a *discriminated union* — much more accurate than Go's "OutMsg with omitempty everywhere", and codegen would force a regression to dumber types.

Revisit when there are 3+ contributors OR the protocol crosses ~20 distinct frame types. Currently 15 outbound + 8 inbound.

### `internal/api` package size is fine

`internal/api` is 21 files / ~3.5 KLOC. The biggest file (`ws.go` at 557 LOC) is dense but cohesive: it's the WS connection loop and its handler dispatch. CONTEXT.md `Quick orientation` correctly routes readers to `claude_handlers.go` for Claude UI specifics — that split is healthy. Don't fragment further.

The one boundary worth watching: `summary_handler.go`/`notes_handler.go` import their domain package for `Path/Dir`. If extraction #3 lands, the handler files won't import the domain package at all (just the validator from it). That's fine.

### `session.Manager` is approaching but not yet a god object

`Manager` is 835 LOC and 25 methods, with 7 dedicated state-pair getters/setters (Mode, Renderer, TemplateID, ClaudeBypass + EnsureClaudeConvoID). It's growing approximately linearly with the number of per-session metadata fields. At ~10 fields the boilerplate-to-logic ratio crosses the line where a `metas[sid].Update(func(*meta) {...})` pattern (extraction #6) becomes mandatory. We're not there yet — fields 8 and 9 will get away with the current shape.

Lifecycle (Create / Close / Reconcile / CreateOrGetRecapSession) is the genuinely complex part and is correctly isolated. The state getters/setters are the "background noise" candidate for #6.

### The CONTEXT.md `Quick orientation` table is itself an extraction signal

The table at `CONTEXT.md:131-163` lists "where to look first for change X" — and several entries are 5+ files long. Specifically the row for "Add a new server-pushed WS frame type" lists 5 files. That's a mechanical cross-cut and the candidate for any future codegen effort, but until codegen lands the table is doing its job.

The row for "Daily recap" at `CONTEXT.md:157` is the longest in the file (a full paragraph). Recap landed late and touched everything — backend kind, manager singleton, dispatcher fast-path, watcher, broadcaster, REST handlers, WS frame, frontend hook integration, sidebar component. The fact that we need a paragraph to navigate it is the cost we pay for not having #1–#4 in place yet.

### `WorkspacePage`'s 3-column layout is doing real work

427 LOC reads alarming but the page genuinely owns three orthogonal concerns: (a) the layout (collapsible left, hidden-able right, persistent widths via `useResizableWidth`), (b) the modal stack (StartClaude, GitCreds, ClaudeCreds, ConfirmClose), and (c) the main-pane dispatch (ClaudeChatView vs ClaudeTerminal vs ChatStream by mode/renderer). Splitting it would just push complexity into a `<WorkspaceLayout>` that takes 10 slot props. Leave it.

---

## What NOT to refactor

### Don't unify the `path.go` helpers in `summary` / `notes` / `recap`

They're 8–10 LOC each, package-doc carries domain-specific behaviour (the notes one explicitly documents "NEVER injected into Claude prompt"; the recap one documents the date-only filename invariant). Centralizing them into `internal/diskdoc/path.go` would lose the per-package documentation and force consumers to import a util package when the current setup lets them say `summary.Path(dataDir, sid)` self-explanatorily.

### Don't extract `MarkdownView` further

`MarkdownView.tsx` is already the shared markdown renderer (consumed by `SummarySection` and `RecapSidebar`). Don't push it deeper — the `ClaudeChatView` markdown rendering is intentionally a different beast (Prism syntax highlight in fenced code blocks, react-markdown with custom components for `<code>`). They share a JSX library, not a use case.

### Don't unify `summary_updated` / `note_updated` / `recap_updated` reducer cases

The first two bump a *per-session* counter (`perSession[sid].summaryFetchCounter`); the third bumps a *top-level* counter on `useSessions` (because recap files are global, not session-scoped). CONTEXT.md `Quick orientation` (last bullet at line 92) documents this explicitly as a deliberate divergence. Don't collapse them — the dimensionality is the bug fix.

### Don't merge `dispatcher_summary.go` and `dispatcher_recap.go` into one file

The *predicate logic* should be extracted (#4 above), but keeping the two files lets each carry its specific package-doc, max-bytes constant, and the rationale comment about why the cap is what it is. The cost is two ~15-LOC files instead of one ~30-LOC file. Worth it for the localized reasoning.

### Don't replace `FindByClaudeConvoID`'s O(N) scan with a map index

`internal/session/manager.go:588-600` already documents this explicitly: "N <= 8, so a map index is over-engineering." Honour it. The hot path is "claude tool call → bridge → dispatcher → manager lookup" and the constant factor on 8 entries is well under 100ns. The hashmap would cost dual-write discipline on every Set/Ensure/Clear of ClaudeSessionID for negative measurable benefit.

### Don't refactor the three composer Enter handlers to share more

The `isSubmitKey` helper at `web/src/lib/keyboard.ts` is the shared bit. The surrounding textarea-vs-input + composition handling in `CommandInput`, `ClaudeChatView`, and `SessionsSidebar` differs because the surrounding UX differs (slash command for one, multi-line vs single-line for the other two). Keep them.

---

## Recommended sequence

1. **#3 (markdown file handler helper)** — smallest, safest, blocking nothing.
2. **#4 (canonical-io predicate helper)** — security surface, do while the design is fresh in head.
3. **#2 (diskwatcher package)** — biggest LOC win; concrete and well-bounded.
4. **#1 (useDocumentSync hook)** — frontend win, unlocks #5.
5. **#6 (finish mutateAndPersist for SetMode/Rename)** — opportunistic cleanup, can ride a future PR.
6. **#5 (split useSessions)** — defer until #1 and a real feature request demand it.

Total LOC delta if all six land: **approximately −350 lines**, with the next feature in each affected area shipping at roughly half the current size.
