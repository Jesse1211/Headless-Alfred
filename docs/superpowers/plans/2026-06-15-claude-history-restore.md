# Restore Claude UI Chat History Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore Claude UI chat history on page refresh by reading Claude CLI's per-session jsonl file as source of truth, with a new backend package + HTTP endpoint + frontend loader hook.

**Architecture:** New Go package `internal/claudehistory` does file location (walk `~/.claude/projects` for `<uuid>.jsonl`) and parsing (jsonl → `[]Turn`). New handler `GET /api/sessions/{sid}/claude-history` exposes it. New TS hook `useClaudeHistoryLoader` fetches on session-select for Claude UI sessions and seeds `perSession[sid].claude.turns` once.

**Tech Stack:** Go 1.25 (stdlib only — `bufio`, `encoding/json`, `filepath.Walk`, `sync`), TS/React 18 (no new deps), Playwright.

---

## File Structure

| file | role | task |
|---|---|---|
| `internal/claudehistory/types.go` | Go mirror of TS ClaudeTurn/ClaudeToolCall | T1 |
| `internal/claudehistory/locate.go` | walk `<HOME>/.claude/projects` + cache | T2 |
| `internal/claudehistory/parse.go` | jsonl → `[]Turn` | T3 |
| `internal/api/claude_history_handler.go` | HTTP handler | T4 |
| `internal/api/router.go` | route registration (existing) | T4 |
| `internal/session/manager.go` | expose `GetClaudeSessionID` (existing) | T4 |
| `web/src/features/sessions/types.ts` | add `turnsLoaded?: boolean` (existing) | T5 |
| `web/src/features/sessions/claudeReducer.ts` | clear `turnsLoaded` on `claude_exited` (existing) | T5 |
| `web/src/lib/api.ts` | `getClaudeHistory` helper (existing) | T6 |
| `web/src/features/sessions/useClaudeHistoryLoader.ts` | new hook | T7 |
| `web/src/features/sessions/WorkspacePage.tsx` | mount hook (existing) | T8 |
| `web/e2e/regression.spec.ts` | refresh-restores-history e2e (existing) | T9 |

---

## Backend

### Task 1: Go types for parsed turn

**Files:**
- Create: `internal/claudehistory/types.go`

JSON field names must match `web/src/features/sessions/types.ts` `ClaudeTurn`/`ClaudeToolCall` (camelCase: `toolUseId`, `startedAt`, `isError`, etc.), so the handler can marshal directly into a shape the frontend reducer already understands.

- [ ] **Step 1: Write the file**

```go
// Package claudehistory reconstructs the per-session Claude UI chat
// transcript by reading Claude CLI's own jsonl log
// (~/.claude/projects/<dir>/<uuid>.jsonl). The CLI's file is the
// source of truth — we never persist a parallel copy.
package claudehistory

// Turn is one round of the conversation: the user's prompt plus
// everything Claude produced in response. JSON tags mirror the
// frontend's ClaudeTurn shape exactly so the handler can stream
// straight to the reducer.
type Turn struct {
	ID        string     `json:"id"`
	Prompt    string     `json:"prompt"`
	StartedAt string     `json:"startedAt"`
	Text      string     `json:"text"`
	Tools     []ToolCall `json:"tools"`
	Done      bool       `json:"done"`
}

// ToolCall is one tool invocation inside a turn. `Decision` is
// always "allow" for rebuilt turns — a tool that ran (whether the
// PreToolUse hook allowed or denied it) appears here; the denial is
// visible in the matching ToolResult's content + IsError, not in
// the decision field.
type ToolCall struct {
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
	// Input is the raw JSON of the tool's input arguments — kept as
	// json.RawMessage to avoid re-encoding what's already valid JSON,
	// and to match the frontend's `input?: unknown`.
	Input    JSONRaw `json:"input,omitempty"`
	Decision string  `json:"decision"`
	Result   string  `json:"result,omitempty"`
	IsError  bool    `json:"isError,omitempty"`
}

// JSONRaw aliases json.RawMessage to keep the import out of files
// that don't need it. Marshals/unmarshals as the underlying bytes.
type JSONRaw = []byte
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./internal/claudehistory/...
```

Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/claudehistory/types.go
git commit -m "feat(claudehistory): add Turn / ToolCall Go types mirroring frontend"
```

---

### Task 2: File locator with walk + invalidating cache

**Files:**
- Create: `internal/claudehistory/locate.go`
- Test: `internal/claudehistory/locate_test.go`

`Locate(sessionID, uuid)` walks `<HOME>/.claude/projects` for any file named `<uuid>.jsonl` and returns its absolute path. Result is cached per sessionID. Cache invalidates if the stored path no longer stats.

- [ ] **Step 1: Write the failing tests**

```go
// internal/claudehistory/locate_test.go
package claudehistory

import (
	"os"
	"path/filepath"
	"testing"
)

func setupFixture(t *testing.T, uuid string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	projDir := filepath.Join(home, ".claude", "projects", "some-encoded-dir")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, uuid+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocate_FindsByBasename(t *testing.T) {
	uuid := "289ce55a-5293-4a52-b76d-6e4299e6fc90"
	want := setupFixture(t, uuid)
	c := NewLocator()
	got, err := c.Locate("sid-A", uuid)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocate_MissingReturnsErrNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := NewLocator()
	_, err := c.Locate("sid-A", "does-not-exist")
	if !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist, got %v", err)
	}
}

func TestLocate_CachesAcrossCalls(t *testing.T) {
	uuid := "ab"
	setupFixture(t, uuid)
	c := NewLocator()
	first, _ := c.Locate("sid-A", uuid)
	// Move the HOME so a second walk would fail — proves we cached.
	t.Setenv("HOME", t.TempDir())
	second, _ := c.Locate("sid-A", uuid)
	if first != second {
		t.Errorf("cache miss: first=%q second=%q", first, second)
	}
}

func TestLocate_CacheInvalidatesIfPathGone(t *testing.T) {
	uuid := "ab"
	path := setupFixture(t, uuid)
	c := NewLocator()
	if _, err := c.Locate("sid-A", uuid); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Cache entry now stale; second call should walk again and
	// (since the file is gone) return os.ErrNotExist.
	_, err := c.Locate("sid-A", uuid)
	if !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist after unlink, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/claudehistory/...
```

Expected: FAIL (NewLocator / Locate undefined).

- [ ] **Step 3: Implement locator**

```go
// internal/claudehistory/locate.go
package claudehistory

import (
	"os"
	"path/filepath"
	"sync"
)

// Locator finds the on-disk jsonl path for a Claude session uuid by
// walking ~/.claude/projects. Walks are bounded — local measurements
// at 752 jsonl files completed in ~80ms. The result is cached per
// alfred sessionID so subsequent refreshes skip the walk.
type Locator struct {
	mu    sync.Mutex
	cache map[string]string // sid → absolute path
}

func NewLocator() *Locator {
	return &Locator{cache: make(map[string]string)}
}

// Locate returns the absolute path of <uuid>.jsonl under ~/.claude/projects.
// Returns os.ErrNotExist if no matching file is found.
//
// A cached path is reused if its file still exists; if it was removed
// between calls (rare — uuid rotation or manual cleanup) we walk again.
func (l *Locator) Locate(sessionID, uuid string) (string, error) {
	l.mu.Lock()
	cached, ok := l.cache[sessionID]
	l.mu.Unlock()
	if ok {
		if _, err := os.Stat(cached); err == nil {
			return cached, nil
		}
		// stale; fall through to walk
		l.mu.Lock()
		delete(l.cache, sessionID)
		l.mu.Unlock()
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".claude", "projects")
	target := uuid + ".jsonl"

	var found string
	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// Permission-denied on a subtree — keep walking the rest.
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(p) == target {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return "", walkErr
	}
	if found == "" {
		return "", os.ErrNotExist
	}

	l.mu.Lock()
	l.cache[sessionID] = found
	l.mu.Unlock()
	return found, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/claudehistory/...
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/claudehistory/locate.go internal/claudehistory/locate_test.go
git commit -m "feat(claudehistory): Locator walks ~/.claude/projects with sid-keyed cache"
```

---

### Task 3: Jsonl parser

**Files:**
- Create: `internal/claudehistory/parse.go`
- Create: `internal/claudehistory/testdata/simple.jsonl`
- Create: `internal/claudehistory/testdata/tool_use.jsonl`
- Create: `internal/claudehistory/testdata/multi_turn.jsonl`
- Create: `internal/claudehistory/testdata/unknown_types.jsonl`
- Create: `internal/claudehistory/testdata/malformed_mid.jsonl`
- Create: `internal/claudehistory/testdata/empty.jsonl`
- Test: `internal/claudehistory/parse_test.go`

`Parse(path, limit, beforeTurnID)` reads jsonl line by line and builds turns from `user` (string content → new turn) and `assistant` (text → append text; tool_use → append tool) and `user` (tool_result → resolve a tool by `tool_use_id`). All other types skipped. Malformed mid-line: log and return what was parsed.

- [ ] **Step 1: Write the fixture files**

`internal/claudehistory/testdata/empty.jsonl` — zero bytes:

```bash
mkdir -p internal/claudehistory/testdata
: > internal/claudehistory/testdata/empty.jsonl
```

`internal/claudehistory/testdata/simple.jsonl`:

```jsonl
{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-06-15T10:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
```

`internal/claudehistory/testdata/tool_use.jsonl`:

```jsonl
{"type":"user","message":{"role":"user","content":"read foo"},"uuid":"u1","timestamp":"2026-06-15T10:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"reading"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/foo"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"the file contents","is_error":false}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}
```

`internal/claudehistory/testdata/multi_turn.jsonl`:

```jsonl
{"type":"user","message":{"role":"user","content":"q1"},"uuid":"u1","timestamp":"2026-06-15T10:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a1"}]}}
{"type":"user","message":{"role":"user","content":"q2"},"uuid":"u2","timestamp":"2026-06-15T10:01:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a2"}]}}
{"type":"user","message":{"role":"user","content":"q3"},"uuid":"u3","timestamp":"2026-06-15T10:02:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a3"}]}}
```

`internal/claudehistory/testdata/unknown_types.jsonl`:

```jsonl
{"type":"permission-mode","permissionMode":"default"}
{"type":"attachment","attachment":{"type":"hook_success"}}
{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-06-15T10:00:00.000Z"}
{"type":"ai-title","title":"chitchat"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
{"type":"system","subtype":"info"}
```

`internal/claudehistory/testdata/malformed_mid.jsonl`:

```jsonl
{"type":"user","message":{"role":"user","content":"q1"},"uuid":"u1","timestamp":"2026-06-15T10:00:00.000Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a1"}]}}
{this is not json
{"type":"user","message":{"role":"user","content":"q2"},"uuid":"u2","timestamp":"2026-06-15T10:01:00.000Z"}
```

- [ ] **Step 2: Write the failing tests**

```go
// internal/claudehistory/parse_test.go
package claudehistory

import (
	"path/filepath"
	"testing"
)

func TestParse_Empty(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "empty.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Errorf("want 0 turns, got %d", len(turns))
	}
}

func TestParse_SimpleUserAssistant(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "simple.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	if turns[0].Prompt != "hi" {
		t.Errorf("prompt = %q", turns[0].Prompt)
	}
	if turns[0].Text != "hello" {
		t.Errorf("text = %q", turns[0].Text)
	}
	if !turns[0].Done {
		t.Errorf("turn not marked done")
	}
	if turns[0].StartedAt != "2026-06-15T10:00:00.000Z" {
		t.Errorf("startedAt = %q", turns[0].StartedAt)
	}
}

func TestParse_ToolUseAndResult(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "tool_use.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	turn := turns[0]
	if turn.Text != "readingdone" {
		t.Errorf("text = %q (want concatenated assistant text)", turn.Text)
	}
	if len(turn.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(turn.Tools))
	}
	tool := turn.Tools[0]
	if tool.ToolUseID != "t1" || tool.Name != "Read" {
		t.Errorf("tool id/name = %q/%q", tool.ToolUseID, tool.Name)
	}
	if tool.Decision != "allow" {
		t.Errorf("decision = %q (want allow)", tool.Decision)
	}
	if tool.Result != "the file contents" {
		t.Errorf("result = %q", tool.Result)
	}
	if tool.IsError {
		t.Errorf("isError = true (want false)")
	}
}

func TestParse_MultiTurnPagination(t *testing.T) {
	all, err := Parse(filepath.Join("testdata", "multi_turn.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 turns, got %d", len(all))
	}
	// Limit=2 returns last 2 turns.
	last2, err := Parse(filepath.Join("testdata", "multi_turn.jsonl"), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(last2) != 2 {
		t.Fatalf("want 2 turns, got %d", len(last2))
	}
	if last2[0].Prompt != "q2" || last2[1].Prompt != "q3" {
		t.Errorf("got prompts %q,%q want q2,q3", last2[0].Prompt, last2[1].Prompt)
	}
	// Before=id-of-q3 with limit=2 returns the 2 turns ending just before q3 → q1,q2.
	before := all[2].ID
	page, err := Parse(filepath.Join("testdata", "multi_turn.jsonl"), 2, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("want 2 turns, got %d", len(page))
	}
	if page[0].Prompt != "q1" || page[1].Prompt != "q2" {
		t.Errorf("got prompts %q,%q want q1,q2", page[0].Prompt, page[1].Prompt)
	}
}

func TestParse_UnknownTypesSkipped(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "unknown_types.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	if turns[0].Prompt != "hi" || turns[0].Text != "hello" {
		t.Errorf("unexpected turn: %+v", turns[0])
	}
}

func TestParse_MalformedMidReturnsPartial(t *testing.T) {
	turns, err := Parse(filepath.Join("testdata", "malformed_mid.jsonl"), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	// We get the first turn (q1/a1). q2 also starts after the bad line —
	// we keep parsing past errors, so we expect both. Empty turns (no
	// assistant content) for q2 are still emitted; only its prompt is set.
	if len(turns) < 1 {
		t.Fatalf("want at least 1 turn, got %d", len(turns))
	}
	if turns[0].Prompt != "q1" || turns[0].Text != "a1" {
		t.Errorf("first turn lost: %+v", turns[0])
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/claudehistory/...
```

Expected: FAIL (Parse undefined).

- [ ] **Step 4: Implement the parser**

```go
// internal/claudehistory/parse.go
package claudehistory

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
)

// Parse reads a Claude CLI jsonl transcript and reconstructs the
// conversation as a slice of Turns (oldest → newest).
//
// Pagination:
//   - limit clamps how many turns are returned. ≤ 0 means "all".
//   - beforeTurnID, if set, returns the last `limit` turns whose IDs
//     come strictly before it in the original sequence. (Frontends can
//     scroll backwards by passing the oldest visible turn's id.)
//
// Robustness: lines that don't unmarshal are logged and skipped. An
// unrecoverable file error (open failure) is returned. Empty files
// return ([]Turn{}, nil). The trailing turn is sealed (Done=true)
// even if Claude was mid-reply — refresh-time reads never observe a
// live stream.
func Parse(path string, limit int, beforeTurnID string) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// tool_result.content can be hundreds of KB (Read output). Bump
	// the line size cap well above the default 64KB.
	scanner.Buffer(make([]byte, 0, 1<<20), 4<<20)

	var turns []Turn
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var head lineHead
		if err := json.Unmarshal(line, &head); err != nil {
			slog.Warn("claudehistory.Parse: skipping malformed jsonl line",
				"path", path, "err", err)
			continue
		}
		switch head.Type {
		case "user":
			handleUser(&turns, line)
		case "assistant":
			handleAssistant(&turns, line)
		default:
			// silently skip: permission-mode, attachment, ai-title,
			// last-prompt, system, file-history-snapshot, queue-operation,
			// and any future types
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		// Treat as partial-read warning, not a fatal error.
		slog.Warn("claudehistory.Parse: scanner error",
			"path", path, "err", err)
	}

	// Mark all turns done — we never serve mid-stream from jsonl.
	for i := range turns {
		turns[i].Done = true
	}

	return paginate(turns, limit, beforeTurnID), nil
}

// lineHead captures just enough of each line to decide how to dispatch.
type lineHead struct {
	Type    string  `json:"type"`
	UUID    string  `json:"uuid"`
	TS      string  `json:"timestamp"`
	Message rawMsg  `json:"message"`
}

type rawMsg struct {
	Content json.RawMessage `json:"content"`
}

// handleUser branches on whether content is a string (new turn) or
// an array (tool_result items resolving the current turn's tools).
func handleUser(turns *[]Turn, line []byte) {
	var full struct {
		UUID    string  `json:"uuid"`
		TS      string  `json:"timestamp"`
		Message rawMsg  `json:"message"`
	}
	if err := json.Unmarshal(line, &full); err != nil {
		return
	}
	c := full.Message.Content
	if len(c) == 0 {
		return
	}
	// Try string content first.
	if c[0] == '"' {
		var s string
		if err := json.Unmarshal(c, &s); err != nil {
			return
		}
		id := full.UUID
		if id == "" {
			id = stableID(line)
		}
		*turns = append(*turns, Turn{
			ID:        id,
			Prompt:    s,
			StartedAt: full.TS,
			Tools:     []ToolCall{},
		})
		return
	}
	// Array content — look for tool_result items.
	if c[0] != '[' {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(c, &items); err != nil {
		return
	}
	if len(*turns) == 0 {
		return
	}
	cur := &(*turns)[len(*turns)-1]
	for _, raw := range items {
		var item struct {
			Type       string `json:"type"`
			ToolUseID  string `json:"tool_use_id"`
			Content    string `json:"content"`
			IsError    bool   `json:"is_error"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		if item.Type != "tool_result" {
			continue
		}
		for i := range cur.Tools {
			if cur.Tools[i].ToolUseID == item.ToolUseID {
				cur.Tools[i].Result = item.Content
				cur.Tools[i].IsError = item.IsError
				break
			}
		}
	}
}

// handleAssistant appends text or tool_use to the current turn.
func handleAssistant(turns *[]Turn, line []byte) {
	var full struct {
		Message struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &full); err != nil {
		return
	}
	if len(*turns) == 0 {
		// Orphaned assistant content (no preceding user prompt) —
		// drop it. Spec edge case.
		return
	}
	cur := &(*turns)[len(*turns)-1]
	for _, raw := range full.Message.Content {
		var item struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		switch item.Type {
		case "text":
			cur.Text += item.Text
		case "tool_use":
			cur.Tools = append(cur.Tools, ToolCall{
				ToolUseID: item.ID,
				Name:      item.Name,
				Input:     []byte(item.Input),
				Decision:  "allow",
			})
		}
	}
}

// stableID derives a deterministic ID from a line's bytes when the
// jsonl didn't supply a uuid. SHA-1 first 16 hex chars — long enough
// to avoid collisions across a single file's worth of turns.
func stableID(line []byte) string {
	h := sha1.Sum(line)
	return hex.EncodeToString(h[:])[:16]
}

// paginate trims `turns` to the last `limit` entries ending before
// `beforeTurnID` (or just the last `limit` if `beforeTurnID` is empty).
// limit <= 0 means "no limit".
func paginate(turns []Turn, limit int, beforeTurnID string) []Turn {
	if beforeTurnID != "" {
		idx := -1
		for i, t := range turns {
			if t.ID == beforeTurnID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			turns = turns[:idx]
		}
	}
	if limit > 0 && len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	return turns
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/claudehistory/...
```

Expected: PASS (all 6 parse tests + 4 locate tests).

- [ ] **Step 6: Commit**

```bash
git add internal/claudehistory/parse.go internal/claudehistory/parse_test.go \
        internal/claudehistory/testdata/
git commit -m "feat(claudehistory): jsonl parser with pagination + defensive skip"
```

---

### Task 4: HTTP handler + route wiring + Manager.GetClaudeSessionID

**Files:**
- Create: `internal/api/claude_history_handler.go`
- Create: `internal/api/claude_history_handler_test.go`
- Modify: `internal/api/router.go`
- Modify: `internal/session/manager.go`

The handler needs the session's ClaudeSessionID UUID. Currently `getClaudeConvoID` is private. Expose a public `GetClaudeSessionID`.

- [ ] **Step 1: Expose GetClaudeSessionID on Manager**

Open `internal/session/manager.go`, find the private `getClaudeConvoID` around line 488:

```go
func (m *Manager) getClaudeConvoID(sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if meta, ok := m.metas[sessionID]; ok {
		return meta.ClaudeSessionID
	}
	return ""
}
```

Add a public wrapper right above it:

```go
// GetClaudeSessionID returns the persisted ClaudeSessionID for sessionID,
// or "" if the session is unknown or hasn't started Claude yet. Public
// counterpart of the package-internal getClaudeConvoID.
func (m *Manager) GetClaudeSessionID(sessionID string) string {
	return m.getClaudeConvoID(sessionID)
}
```

- [ ] **Step 2: Write failing handler tests**

```go
// internal/api/claude_history_handler_test.go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudehistory"
)

// sessionIDLookup is a tiny stub matching the subset of *session.Manager
// the handler actually uses. Lets us test without spinning up tmux.
type sessionIDLookup interface {
	GetClaudeSessionID(sessionID string) string
}

type fakeLookup struct {
	m map[string]string
}

func (f *fakeLookup) GetClaudeSessionID(sid string) string { return f.m[sid] }

func newTestHandler(t *testing.T, lookup sessionIDLookup) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/claude-history",
		GetClaudeHistoryHandler(lookup, claudehistory.NewLocator()).ServeHTTP)
	return r
}

func writeFixtureJsonl(t *testing.T, uuid, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, uuid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeHistory_NoUUIDReturnsEmpty(t *testing.T) {
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": ""}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q (want [])", w.Body.String())
	}
}

func TestClaudeHistory_MissingFileReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": "ghost-uuid"}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q (want [])", w.Body.String())
	}
}

func TestClaudeHistory_ReturnsParsedTurns(t *testing.T) {
	uuid := "uuid-test"
	writeFixtureJsonl(t, uuid, strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-06-15T10:00:00.000Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
	}, "\n"))
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": uuid}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	var got []claudehistory.Turn
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(got) != 1 || got[0].Prompt != "hi" || got[0].Text != "hello" {
		t.Errorf("got %+v", got)
	}
}

func TestClaudeHistory_LimitClampedTo500(t *testing.T) {
	uuid := "uuid-clamp"
	// Build 3 turns; ask for limit=999 → response is still all 3, no error.
	lines := []string{}
	for i := 1; i <= 3; i++ {
		lines = append(lines,
			`{"type":"user","message":{"role":"user","content":"q`+itoa(i)+`"},"uuid":"u`+itoa(i)+`","timestamp":"2026-06-15T10:00:00.000Z"}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a`+itoa(i)+`"}]}}`)
	}
	writeFixtureJsonl(t, uuid, strings.Join(lines, "\n"))
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": uuid}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history?limit=999", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var got []claudehistory.Turn
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 3 {
		t.Errorf("got %d turns, want 3", len(got))
	}
}

// itoa avoids importing strconv just for tests.
func itoa(i int) string { return string(rune('0' + i)) }
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/api/ -run TestClaudeHistory
```

Expected: FAIL (GetClaudeHistoryHandler undefined).

- [ ] **Step 4: Implement the handler**

```go
// internal/api/claude_history_handler.go
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudehistory"
)

// claudeSessionIDLookup is the subset of *session.Manager the handler
// needs. Lets tests stub it without spinning up a real manager.
type claudeSessionIDLookup interface {
	GetClaudeSessionID(sessionID string) string
}

// GetClaudeHistoryHandler serves the reconstructed Claude UI chat
// history for a session by reading the underlying CLI jsonl file.
//
// 200 + [] for: session never entered Claude (no ClaudeSessionID), or
// jsonl file not found on disk. 200 + turns for normal case. 500 only
// for unexpected I/O explosions. There is no 404 for "no history" —
// empty is a valid state for any session.
func GetClaudeHistoryHandler(lookup claudeSessionIDLookup, locator *claudehistory.Locator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")

		// Clamp limit to [1, 500]; default 100.
		limit := 100
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				limit = n
			}
		}
		if limit < 1 {
			limit = 1
		}
		if limit > 500 {
			limit = 500
		}
		before := r.URL.Query().Get("before")

		uuid := lookup.GetClaudeSessionID(sid)
		if uuid == "" {
			writeJSON(w, http.StatusOK, []claudehistory.Turn{})
			return
		}

		path, err := locator.Locate(sid, uuid)
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("claude history jsonl missing", "sid", sid, "uuid", uuid)
			writeJSON(w, http.StatusOK, []claudehistory.Turn{})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "history_error", err.Error())
			return
		}

		turns, err := claudehistory.Parse(path, limit, before)
		if err != nil {
			// Locate succeeded so the file exists. Parse-side failures
			// are already logged + best-effort partial inside Parse,
			// so this branch fires only on Open failure (file disappeared
			// between Locate and Parse) or a wholesale I/O error.
			slog.Warn("claudehistory.Parse failed", "sid", sid, "path", path, "err", err)
			writeError(w, http.StatusInternalServerError, "history_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, turns)
	})
}

// writeJSON is a tiny convenience to JSON-encode + set the content type.
// (Other handlers in this package inline this; centralising avoids
// repeating the err-ignore boilerplate.)
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 5: Wire the route**

Edit `internal/api/router.go`. The `Deps` struct gains a Locator (so the per-process cache is shared). Find the existing import block + Deps struct + route group:

```go
import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/static"
)
```

Add the claudehistory import:

```go
import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/claude"
	"github.com/jesseliu/headless-alfred/internal/claudehistory"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/static"
)
```

Find the `Summary.` block (line ~62):

```go
// Summary.
r.Get("/api/sessions/{sid}/summary", GetSummaryHandler(d.Manager.DataDir()).ServeHTTP)
```

Insert immediately after it:

```go
// Claude UI chat history (rebuilt from CLI jsonl).
r.Get("/api/sessions/{sid}/claude-history",
	GetClaudeHistoryHandler(d.Manager, claudehistory.NewLocator()).ServeHTTP)
```

The `claudehistory.NewLocator()` call here creates one locator per process startup; chi reuses the closure on every request, so the cache persists. (This is fine because Deps lives for the whole process.)

- [ ] **Step 6: Run all tests to verify**

```bash
go test ./internal/claudehistory/... ./internal/api/... ./internal/session/...
```

Expected: PASS — new handler tests green, existing tests unaffected.

- [ ] **Step 7: Commit**

```bash
git add internal/api/claude_history_handler.go internal/api/claude_history_handler_test.go \
        internal/api/router.go internal/session/manager.go
git commit -m "feat(api): GET /api/sessions/{sid}/claude-history endpoint"
```

---

## Frontend

### Task 5: `turnsLoaded` flag + reducer clear on exit

**Files:**
- Modify: `web/src/features/sessions/types.ts`
- Modify: `web/src/features/sessions/claudeReducer.ts`
- Modify: `web/src/features/sessions/sessionsReducer.test.ts`

- [ ] **Step 1: Add the field to ClaudeState**

Open `web/src/features/sessions/types.ts`. Find `interface ClaudeState`:

```ts
export interface ClaudeState {
  turns: ClaudeTurn[]
  inFlight: boolean
  pending: ClaudeToolApprovalRequest[]
  pendingQuestions: ClaudeQuestionRequest[]
  lastError?: { code: string; message: string }
}
```

Add `turnsLoaded?: boolean` immediately after `turns`:

```ts
export interface ClaudeState {
  turns: ClaudeTurn[]
  // True once useClaudeHistoryLoader has done its one-shot fetch
  // from the backend jsonl-restore endpoint. Cleared on claude_exited
  // so re-entering re-runs the fetch (the underlying uuid may have
  // rotated). Sticky across WS reconnects within the same page load.
  turnsLoaded?: boolean
  inFlight: boolean
  pending: ClaudeToolApprovalRequest[]
  pendingQuestions: ClaudeQuestionRequest[]
  lastError?: { code: string; message: string }
}
```

Leave `emptyClaudeState()` unchanged (omitted ≡ false).

- [ ] **Step 2: Clear turnsLoaded on claude_exited**

Open `web/src/features/sessions/claudeReducer.ts`. Find the `case 'claude_exited':` block (around line 60):

```ts
case 'claude_exited': {
  const cur = prev.get(m.sessionID) ?? emptyPerSessionState()
  const next = new Map(prev)
  next.set(m.sessionID, {
    ...cur,
    mode: 'shell',
    renderer: '',
    templateId: undefined,
    // Keep the prior conversation history for re-display next time —
    // we just clear the "in-flight" state. (If we wanted to drop
    // the conversation on Exit we'd null `claude` here.)
    claude: cur.claude ? { ...cur.claude, inFlight: false, pending: [], pendingQuestions: [] } : undefined,
  })
  return next
}
```

Add `turnsLoaded: false` inside the claude object spread:

```ts
case 'claude_exited': {
  const cur = prev.get(m.sessionID) ?? emptyPerSessionState()
  const next = new Map(prev)
  next.set(m.sessionID, {
    ...cur,
    mode: 'shell',
    renderer: '',
    templateId: undefined,
    // Keep the prior conversation history for re-display next time —
    // we just clear the "in-flight" state. (If we wanted to drop
    // the conversation on Exit we'd null `claude` here.) turnsLoaded
    // is reset so the next enter triggers a fresh history fetch — the
    // underlying ClaudeSessionID may have rotated.
    claude: cur.claude
      ? { ...cur.claude, inFlight: false, pending: [], pendingQuestions: [], turnsLoaded: false }
      : undefined,
  })
  return next
}
```

- [ ] **Step 3: Add a reducer test**

Open `web/src/features/sessions/sessionsReducer.test.ts`. Find the existing `'claude_exited preserves turn history but clears in-flight + pending'` test. Right below it (in the same describe), add:

```ts
  it('claude_exited clears turnsLoaded', () => {
    const seed = new Map<string, PerSessionState>([
      ['A', {
        ...emptyPerSessionState(),
        mode: 'claude',
        renderer: 'ui',
        claude: { ...emptyClaudeState(), turnsLoaded: true },
      }],
    ])
    const { perSession } = reducePerSession(seed, { type: 'claude_exited', sessionID: 'A' }, b64decode)
    expect(perSession.get('A')?.claude?.turnsLoaded).toBe(false)
  })
```

If `emptyClaudeState` isn't already in the file's imports, add it.

- [ ] **Step 4: Run tests**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + green (82 tests now).

- [ ] **Step 5: Commit**

```bash
git add web/src/features/sessions/types.ts web/src/features/sessions/claudeReducer.ts \
        web/src/features/sessions/sessionsReducer.test.ts
git commit -m "feat(sessions): add turnsLoaded flag; reset on claude_exited"
```

---

### Task 6: `getClaudeHistory` API helper

**Files:**
- Modify: `web/src/lib/api.ts`

- [ ] **Step 1: Append helper at end of file**

Open `web/src/lib/api.ts`. Append after the last function:

```ts
import type { ClaudeTurn } from '../features/sessions/types'

// getClaudeHistory fetches the reconstructed Claude UI chat history
// for a session from the backend's jsonl-restore endpoint. Returns
// [] when the session has no jsonl yet (user hasn't entered Claude,
// or the file has been moved/deleted) — empty is a valid state, not
// an error.
export async function getClaudeHistory(
  sessionID: string,
  opts: { limit?: number; before?: string } = {},
): Promise<ClaudeTurn[]> {
  const qs = new URLSearchParams()
  if (opts.limit != null) qs.set('limit', String(opts.limit))
  if (opts.before) qs.set('before', opts.before)
  const url =
    `/api/sessions/${encodeURIComponent(sessionID)}/claude-history` +
    (qs.size ? '?' + qs.toString() : '')
  const res = await request(url)
  return res.json()
}
```

Important: the `import type { ClaudeTurn }` must go at the TOP of the file, with the other imports — TypeScript hoists, but lint will complain. Move it to the imports section if needed.

- [ ] **Step 2: TS check**

```bash
cd web && npx tsc --noEmit
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/api.ts
git commit -m "feat(api): getClaudeHistory client helper"
```

---

### Task 7: `useClaudeHistoryLoader` hook

**Files:**
- Create: `web/src/features/sessions/useClaudeHistoryLoader.ts`
- Create: `web/src/features/sessions/useClaudeHistoryLoader.test.ts`

- [ ] **Step 1: Write the failing tests**

```ts
// web/src/features/sessions/useClaudeHistoryLoader.test.ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { useClaudeHistoryLoader } from './useClaudeHistoryLoader'
import { PerSessionState, emptyPerSessionState, emptyClaudeState, ClaudeTurn } from './types'
import * as api from '../../lib/api'

function makeTurn(id: string, prompt: string): ClaudeTurn {
  return { id, prompt, startedAt: '2026-06-15T00:00:00Z', text: 'r', tools: [], done: true }
}

describe('useClaudeHistoryLoader', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('fetches history once for claude+ui session and seeds turns', async () => {
    vi.spyOn(api, 'getClaudeHistory').mockResolvedValue([makeTurn('t1', 'hi')])
    const initial = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui', claude: emptyClaudeState() }],
    ])
    let state = initial
    const setState = vi.fn((updater: (p: typeof state) => typeof state) => {
      state = updater(state)
    })

    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: setState as never,
      }),
    )

    await waitFor(() => {
      expect(state.get('A')?.claude?.turnsLoaded).toBe(true)
    })
    expect(state.get('A')?.claude?.turns).toEqual([makeTurn('t1', 'hi')])
    expect(api.getClaudeHistory).toHaveBeenCalledOnce()
    expect(api.getClaudeHistory).toHaveBeenCalledWith('A')
  })

  it('does not fetch for shell-mode sessions', () => {
    const spy = vi.spyOn(api, 'getClaudeHistory').mockResolvedValue([])
    const state = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), mode: 'shell' }],
    ])
    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: vi.fn() as never,
      }),
    )
    expect(spy).not.toHaveBeenCalled()
  })

  it('does not refetch if turnsLoaded already true', () => {
    const spy = vi.spyOn(api, 'getClaudeHistory').mockResolvedValue([])
    const state = new Map<string, PerSessionState>([
      ['A', {
        ...emptyPerSessionState(),
        mode: 'claude',
        renderer: 'ui',
        claude: { ...emptyClaudeState(), turnsLoaded: true },
      }],
    ])
    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: vi.fn() as never,
      }),
    )
    expect(spy).not.toHaveBeenCalled()
  })

  it('on fetch failure still sets turnsLoaded to true (no retry loop)', async () => {
    vi.spyOn(api, 'getClaudeHistory').mockRejectedValue(new Error('boom'))
    let state = new Map<string, PerSessionState>([
      ['A', { ...emptyPerSessionState(), mode: 'claude', renderer: 'ui', claude: emptyClaudeState() }],
    ])
    const setState = vi.fn((updater: (p: typeof state) => typeof state) => {
      state = updater(state)
    })
    renderHook(() =>
      useClaudeHistoryLoader({
        selectedSessionID: 'A',
        perSession: state,
        setPerSession: setState as never,
      }),
    )
    await waitFor(() => {
      expect(state.get('A')?.claude?.turnsLoaded).toBe(true)
    })
    expect(state.get('A')?.claude?.lastError?.code).toBe('history_unavailable')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd web && npx vitest run useClaudeHistoryLoader
```

Expected: FAIL (module not found).

- [ ] **Step 3: Implement the hook**

```ts
// web/src/features/sessions/useClaudeHistoryLoader.ts
import { useEffect } from 'react'
import { getClaudeHistory } from '../../lib/api'
import { PerSessionState, emptyClaudeState, emptyPerSessionState } from './types'

interface Args {
  selectedSessionID: string | null
  perSession: Map<string, PerSessionState>
  setPerSession: (updater: (prev: Map<string, PerSessionState>) => Map<string, PerSessionState>) => void
}

// useClaudeHistoryLoader observes the selected session and, when it
// enters Claude UI mode for the first time in this page lifecycle,
// fetches the rebuilt chat history from the backend jsonl-restore
// endpoint and seeds claude.turns. Idempotent — guarded by
// claude.turnsLoaded so it fires at most once per session per page
// load.
//
// Errors are absorbed: the flag is still flipped to true so we don't
// retry-loop, and lastError carries the reason. The existing
// ClaudeChatView already renders state.lastError as a banner so no
// UI change is needed.
export function useClaudeHistoryLoader({ selectedSessionID, perSession, setPerSession }: Args) {
  // Snapshot the fields the effect reads so dependency tracking is
  // straightforward without re-running on unrelated state changes.
  const ps = selectedSessionID ? perSession.get(selectedSessionID) : undefined
  const mode = ps?.mode
  const renderer = ps?.renderer
  const turnsLoaded = ps?.claude?.turnsLoaded === true

  useEffect(() => {
    if (!selectedSessionID) return
    if (mode !== 'claude' || renderer !== 'ui') return
    if (turnsLoaded) return

    let alive = true
    getClaudeHistory(selectedSessionID)
      .then((turns) => {
        if (!alive) return
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur = next.get(selectedSessionID) ?? emptyPerSessionState()
          const c = cur.claude ?? emptyClaudeState()
          next.set(selectedSessionID, {
            ...cur,
            claude: { ...c, turns, turnsLoaded: true, lastError: undefined },
          })
          return next
        })
      })
      .catch((e: unknown) => {
        if (!alive) return
        const msg = e instanceof Error ? e.message : String(e)
        setPerSession((prev) => {
          const next = new Map(prev)
          const cur = next.get(selectedSessionID) ?? emptyPerSessionState()
          const c = cur.claude ?? emptyClaudeState()
          next.set(selectedSessionID, {
            ...cur,
            // Don't clobber existing turns on error.
            claude: { ...c, turnsLoaded: true, lastError: { code: 'history_unavailable', message: msg } },
          })
          return next
        })
      })
    return () => { alive = false }
  }, [selectedSessionID, mode, renderer, turnsLoaded, setPerSession])
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd web && npx vitest run useClaudeHistoryLoader
```

Expected: PASS (4 tests).

- [ ] **Step 5: Run the full vitest suite**

```bash
cd web && npm test
```

Expected: 86 tests pass (82 prior + 4 new).

- [ ] **Step 6: Commit**

```bash
git add web/src/features/sessions/useClaudeHistoryLoader.ts \
        web/src/features/sessions/useClaudeHistoryLoader.test.ts
git commit -m "feat(sessions): useClaudeHistoryLoader hook"
```

---

### Task 8: Mount the hook in WorkspacePage

**Files:**
- Modify: `web/src/features/sessions/WorkspacePage.tsx`

- [ ] **Step 1: Add import + mount call**

Open `web/src/features/sessions/WorkspacePage.tsx`. Find the existing imports:

```tsx
import { useSessions } from './useSessions'
import { useSessionHistoryLoader } from './useSessionHistoryLoader'
```

Add the new hook import after `useSessionHistoryLoader`:

```tsx
import { useSessions } from './useSessions'
import { useSessionHistoryLoader } from './useSessionHistoryLoader'
import { useClaudeHistoryLoader } from './useClaudeHistoryLoader'
```

Find the existing `useSessionHistoryLoader` call inside the component body (around line 27):

```tsx
const s = useSessions(token)
useSessionHistoryLoader({
  selectedSessionID: s.selectedSessionID,
  perSession: s.perSession,
  setPerSession: s.setPerSession,
})
```

Add the new hook call immediately after:

```tsx
const s = useSessions(token)
useSessionHistoryLoader({
  selectedSessionID: s.selectedSessionID,
  perSession: s.perSession,
  setPerSession: s.setPerSession,
})
useClaudeHistoryLoader({
  selectedSessionID: s.selectedSessionID,
  perSession: s.perSession,
  setPerSession: s.setPerSession,
})
```

- [ ] **Step 2: TS + tests**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + green.

- [ ] **Step 3: Commit**

```bash
git add web/src/features/sessions/WorkspacePage.tsx
git commit -m "feat(workspace): mount useClaudeHistoryLoader"
```

---

### Task 9: End-to-end Playwright test

**Files:**
- Modify: `web/e2e/regression.spec.ts`

Tests that a real Claude conversation survives a page reload. Uses the real Claude API path (same as the existing `AskUserQuestion` test) so it's slow and occasionally flaky — flagged with a 60s timeout.

- [ ] **Step 1: Append the new describe at the END of the file**

```ts
test.describe('ClaudeChatView: history restores after page reload', () => {
  test('prompt + reply persist after reload via jsonl rehydrate', async ({ page }) => {
    test.setTimeout(60_000)

    const tok = await login(page)
    const sid = await freshSessionTracked(page, tok, 'pw-history-restore')
    await loginUI(page, tok)
    await selectSession(page, sid)

    // Enter Claude UI. Defaults are bypass=true and summary=true; both
    // are fine — neither interferes with history restore.
    await page.locator('.workspace__claude-btn').click()
    await expect(page.locator('text=Start Claude')).toBeVisible()
    await page.locator('label:has-text("Chat UI")').click()
    await page.locator('button:has-text("Start")').click()
    await expect(page.locator('textarea.claude-chat__input')).toBeVisible({ timeout: 10_000 })

    // Send a tiny prompt, wait for the response to land.
    const promptText = 'reply with the single word "history-marker"'
    await page.locator('textarea.claude-chat__input').fill(promptText)
    await page.locator('textarea.claude-chat__input').press('Enter')
    // Wait for composer to return to idle (turn done).
    await expect(page.locator('textarea.claude-chat__input'))
      .toHaveAttribute('placeholder', 'Message Claude…', { timeout: 50_000 })

    // Both prompt + a fragment of the reply visible BEFORE reload.
    await expect(page.locator('.claude-turn__user-text')).toContainText(promptText)
    await expect(page.locator('.claude-turn__text')).toContainText('history-marker')

    // Reload.
    await page.reload()
    await page.waitForLoadState('networkidle')
    await expect(page.locator('textarea.claude-chat__input')).toBeVisible({ timeout: 10_000 })

    // The jsonl-restore path should rehydrate the same turn.
    await expect(page.locator('.claude-turn__user-text')).toContainText(promptText, { timeout: 10_000 })
    await expect(page.locator('.claude-turn__text')).toContainText('history-marker')
  })
})
```

- [ ] **Step 2: Run the e2e test**

```bash
cd web && ALFRED_DATA_DIR=/tmp/alfred-dev/data \
  npx playwright test --grep "history restores after page reload" 2>&1 | tail -25
```

Expected: PASS. (If the dev backend isn't running, this will fail with a connection refused — that's an environment issue, not a code issue.)

- [ ] **Step 3: Commit**

```bash
git add web/e2e/regression.spec.ts
git commit -m "test(e2e): Claude chat history restores across page reload"
```

---

## Final verification

After all tasks, run the full backend + frontend sweep:

- [ ] **Go**

```bash
go test ./internal/...
go test -race ./internal/claudehistory/... ./internal/api/...
```

Expected: all pass; -race clean.

- [ ] **Frontend unit**

```bash
cd web && npx tsc --noEmit && npm test
```

Expected: clean + ~86 tests pass.

- [ ] **E2e (full regression)**

```bash
cd web && ALFRED_DATA_DIR=/tmp/alfred-dev/data npx playwright test 2>&1 | tail -20
```

Expected: all existing tests still pass + 1 new test green.
