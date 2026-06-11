# Multi-session Plan 5 — REST API (/api/sessions* + /api/sessions/{id}/commands*)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single-bash REST surface with multi-session endpoints. New: 4 endpoints on `/api/sessions`. Moved: 3 commands endpoints become `/api/sessions/{id}/commands*`. Old `/api/commands*` endpoints are deleted (no backwards compatibility per spec §6.3).

**Architecture:** New `internal/api/sessions.go` handler file holds session CRUD. `internal/api/commands.go` is rewritten in place to take `*session.Manager` instead of `*shell.Shell` + `*store.Store`, and reads/writes through `Manager.Get(sessionID)` for per-session shell access. The router (`router.go`) wires both groups.

**Tech Stack:** stdlib + chi router (unchanged) + the four packages from Plans 1-4.

**Spec sections covered:** §6.1 (REST endpoints), §6.3 (no backwards compat).

---

## File Structure

```
internal/api/
├── router.go            # MODIFY: new routes, new Deps shape
├── commands.go          # REWRITE: every handler keyed by sessionID
├── commands_test.go     # MODIFY (or REWRITE): test against Manager-based handlers
├── sessions.go          # NEW: List/Create/Rename/Delete handlers
├── sessions_test.go     # NEW
├── login.go             # UNCHANGED
├── middleware.go        # UNCHANGED
├── errors.go            # UNCHANGED
└── ws.go                # NOT TOUCHED in this plan; Plan 6 rewrites it
```

---

## Task 1: List + Create handlers

**Files:**
- Create: `internal/api/sessions.go`
- Create: `internal/api/sessions_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/api/sessions_test.go`:

```go
package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
	"github.com/jesseliu/headless-alfred/internal/store"
)

func newTestManager(t *testing.T) *session.Manager {
	t.Helper()
	dir := t.TempDir()
	st, _ := store.New(dir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, err := session.NewManager(session.Config{
		DataDir:      dir,
		Store:        st,
		SessionsFile: store.NewSessionsFile(dir),
		Runner:       tmuxio.NewFakeRunner(),
		Nonce:        "test-nonce",
		MaxSessions:  8,
		Logger:       logger,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestListSessions_EmptyReturnsEmptyArray(t *testing.T) {
	m := newTestManager(t)
	h := ListSessionsHandler(m)
	req := httptest.NewRequest("GET", "/api/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	if got := rec.Body.String(); got != "[]\n" {
		t.Fatalf("body = %q, want [] empty array", got)
	}
}

func TestListSessions_ReturnsCreatedSessions(t *testing.T) {
	m := newTestManager(t)
	a, _ := m.Create("alpha")
	b, _ := m.Create("beta")
	h := ListSessionsHandler(m)
	req := httptest.NewRequest("GET", "/api/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got []store.SessionMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	// Order: creation-ascending (so a comes before b).
	if got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("order: %+v", got)
	}
}

func TestCreateSession_NoBodyAutoNames(t *testing.T) {
	m := newTestManager(t)
	h := CreateSessionHandler(m)
	req := httptest.NewRequest("POST", "/api/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var meta store.SessionMeta
	_ = json.Unmarshal(rec.Body.Bytes(), &meta)
	if meta.Name != "Session 1" {
		t.Fatalf("name = %q", meta.Name)
	}
}

func TestCreateSession_NamedBody(t *testing.T) {
	m := newTestManager(t)
	h := CreateSessionHandler(m)
	body := `{"name":"training"}`
	req := httptest.NewRequest("POST", "/api/sessions", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("code = %d", rec.Code)
	}
	var meta store.SessionMeta
	_ = json.Unmarshal(rec.Body.Bytes(), &meta)
	if meta.Name != "training" {
		t.Fatalf("name = %q", meta.Name)
	}
}

func TestCreateSession_LimitReturns422(t *testing.T) {
	m := newTestManager(t)
	for i := 0; i < 8; i++ {
		_, _ = m.Create("")
	}
	h := CreateSessionHandler(m)
	req := httptest.NewRequest("POST", "/api/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("session_limit")) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestCreateSession_BadNameReturns422(t *testing.T) {
	m := newTestManager(t)
	h := CreateSessionHandler(m)
	long := `{"name":"`
	for i := 0; i < 200; i++ {
		long += "a"
	}
	long += `"}`
	req := httptest.NewRequest("POST", "/api/sessions", bytes.NewBufferString(long))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/api/ -run "TestListSessions|TestCreateSession" -count=1`
Expected: BUILD FAILS on undefined handlers.

- [ ] **Step 3: Implement List + Create**

Create `internal/api/sessions.go`:

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// ListSessionsHandler: GET /api/sessions
// Returns [] when empty (never null).
func ListSessionsHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := m.List()
		if list == nil {
			list = []store.SessionMeta{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})
}

// CreateSessionHandler: POST /api/sessions
// Body: { "name"?: string } — missing/empty triggers auto-naming.
// Responses:
//   201 + SessionMeta on success
//   422 session_limit when MaxSessions is reached
//   422 bad_name on over-length name
//   400 bad_request on malformed JSON body
func CreateSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		// Empty body is OK (auto-name).
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
				return
			}
		}
		meta, err := m.Create(req.Name)
		switch {
		case errors.Is(err, session.ErrSessionLimit):
			writeError(w, http.StatusUnprocessableEntity, "session_limit", "session limit reached")
			return
		case errors.Is(err, session.ErrBadName):
			writeError(w, http.StatusUnprocessableEntity, "bad_name", "session name is empty or too long")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "create_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(meta)
	})
}
```

- [ ] **Step 4: Run, confirm green**

Run: `go test ./internal/api/ -run "TestListSessions|TestCreateSession" -count=1 -v`
Expected: 6 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/sessions.go internal/api/sessions_test.go
git commit -m "api: ListSessions + CreateSession handlers (with 422 session_limit + bad_name)"
```

---

## Task 2: Rename + Delete handlers

**Files:**
- Modify: `internal/api/sessions.go`
- Modify: `internal/api/sessions_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/api/sessions_test.go`:

```go
func TestRenameSession_Success(t *testing.T) {
	m := newTestManager(t)
	meta, _ := m.Create("Session 1")
	h := RenameSessionHandler(m)
	body := `{"name":"training"}`
	req := httptest.NewRequest("PATCH", "/api/sessions/"+meta.ID, bytes.NewBufferString(body))
	req = mustChi(req, "id", meta.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	if m.List()[0].Name != "training" {
		t.Fatalf("name not updated: %+v", m.List()[0])
	}
}

func TestRenameSession_NotFound(t *testing.T) {
	m := newTestManager(t)
	h := RenameSessionHandler(m)
	body := `{"name":"x"}`
	req := httptest.NewRequest("PATCH", "/api/sessions/nope", bytes.NewBufferString(body))
	req = mustChi(req, "id", "nope")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestRenameSession_BadName(t *testing.T) {
	m := newTestManager(t)
	meta, _ := m.Create("Session 1")
	h := RenameSessionHandler(m)
	body := `{"name":"   "}`
	req := httptest.NewRequest("PATCH", "/api/sessions/"+meta.ID, bytes.NewBufferString(body))
	req = mustChi(req, "id", meta.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestDeleteSession_Success(t *testing.T) {
	m := newTestManager(t)
	meta, _ := m.Create("Session 1")
	h := DeleteSessionHandler(m)
	req := httptest.NewRequest("DELETE", "/api/sessions/"+meta.ID, nil)
	req = mustChi(req, "id", meta.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("code = %d", rec.Code)
	}
	if len(m.List()) != 0 {
		t.Fatalf("session not deleted")
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	m := newTestManager(t)
	h := DeleteSessionHandler(m)
	req := httptest.NewRequest("DELETE", "/api/sessions/nope", nil)
	req = mustChi(req, "id", "nope")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("code = %d", rec.Code)
	}
}

// mustChi sets a chi URL param on the request context for handler tests
// that bypass the router.
func mustChi(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
```

Add the test imports at the top of the file:
```go
import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell/tmuxio"
	"github.com/jesseliu/headless-alfred/internal/store"
)
```

- [ ] **Step 2: Run, confirm build error**

Run: `go test ./internal/api/ -run "Test(Rename|Delete)Session" -count=1`
Expected: BUILD FAILS.

- [ ] **Step 3: Implement Rename + Delete**

Append to `internal/api/sessions.go`:

```go
import (
	// add to top:
	"github.com/go-chi/chi/v5"
)

// RenameSessionHandler: PATCH /api/sessions/{id}
// Body: { "name": string }
//   200 on success
//   404 not_found
//   422 bad_name (empty/over-length)
//   400 bad_request (malformed body)
func RenameSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
			return
		}
		err := m.Rename(id, req.Name)
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		case errors.Is(err, session.ErrBadName):
			writeError(w, http.StatusUnprocessableEntity, "bad_name", "session name is empty or too long")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "rename_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// DeleteSessionHandler: DELETE /api/sessions/{id}
//   204 on success
//   404 not_found
func DeleteSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		err := m.Close(id)
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
```

- [ ] **Step 4: Run, confirm green**

Run: `go test ./internal/api/ -run "TestSession|TestListSessions|TestCreateSession|Test(Rename|Delete)Session" -count=1 -v`
Expected: 11 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/sessions.go internal/api/sessions_test.go
git commit -m "api: RenameSession + DeleteSession handlers"
```

---

## Task 3: Rewrite commands handlers to be session-scoped

Old: `GET /api/commands`, `GET /api/commands/{id}`, `POST /api/commands/{id}/stop`.
New: `GET /api/sessions/{sid}/commands`, `GET /api/sessions/{sid}/commands/{id}`, `POST /api/sessions/{sid}/commands/{id}/stop`.

**Files:**
- Replace: `internal/api/commands.go`
- Replace: `internal/api/commands_test.go` (or delete + reimplement)

- [ ] **Step 1: Write the new tests**

Replace `internal/api/commands_test.go` with:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/store"
)

func TestListCommandsForSession_ReturnsEmptyArrayOnUnknownSession(t *testing.T) {
	m := newTestManager(t)
	h := ListCommandsHandler(m)
	req := httptest.NewRequest("GET", "/api/sessions/unknown/commands", nil)
	req = mustChi(req, "sid", "unknown")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	if rec.Body.String() != "[]\n" {
		t.Fatalf("body = %q, want []", rec.Body.String())
	}
}

func TestListCommandsForSession_ReturnsCommandsScopedToSession(t *testing.T) {
	m := newTestManager(t)
	sessA, _ := m.Create("A")
	sessB, _ := m.Create("B")
	now := time.Now().UTC()
	_ = m.StoreFor().Save(sessA.ID, store.Record{
		ID: "1", SessionID: sessA.ID, Command: "ls",
		Status: store.StatusCompleted, StartedAt: now,
	})
	_ = m.StoreFor().Save(sessB.ID, store.Record{
		ID: "2", SessionID: sessB.ID, Command: "pwd",
		Status: store.StatusCompleted, StartedAt: now.Add(time.Second),
	})
	h := ListCommandsHandler(m)
	req := httptest.NewRequest("GET", "/api/sessions/"+sessA.ID+"/commands", nil)
	req = mustChi(req, "sid", sessA.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got []store.Record
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got %+v, want [{id:1}]", got)
	}
}

func TestGetCommand_RetrievesFullRecord(t *testing.T) {
	m := newTestManager(t)
	sess, _ := m.Create("A")
	_ = m.StoreFor().Save(sess.ID, store.Record{
		ID: "1", SessionID: sess.ID, Command: "ls",
		Status: store.StatusCompleted, StartedAt: time.Now().UTC(),
	})
	_ = m.StoreFor().WriteOutput(sess.ID, "1", []byte("foo\nbar\n"))
	h := GetCommandHandler(m)
	req := httptest.NewRequest("GET", "/api/sessions/"+sess.ID+"/commands/1", nil)
	req = mustChi(req, "sid", sess.ID)
	req = mustChi(req, "id", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["output"] != "foo\nbar\n" {
		t.Fatalf("output = %v", got["output"])
	}
}

func TestGetCommand_NotFound(t *testing.T) {
	m := newTestManager(t)
	sess, _ := m.Create("A")
	h := GetCommandHandler(m)
	req := httptest.NewRequest("GET", "/api/sessions/"+sess.ID+"/commands/nope", nil)
	req = mustChi(req, "sid", sess.ID)
	req = mustChi(req, "id", "nope")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestStopCommand_UnknownSessionReturns404(t *testing.T) {
	m := newTestManager(t)
	h := StopCommandHandler(m)
	req := httptest.NewRequest("POST", "/api/sessions/nope/commands/1/stop", nil)
	req = mustChi(req, "sid", "nope")
	req = mustChi(req, "id", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestStopCommand_CommandNotRunningReturns409(t *testing.T) {
	m := newTestManager(t)
	sess, _ := m.Create("A")
	h := StopCommandHandler(m)
	req := httptest.NewRequest("POST", "/api/sessions/"+sess.ID+"/commands/missing/stop", nil)
	req = mustChi(req, "sid", sess.ID)
	req = mustChi(req, "id", "missing")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d", rec.Code)
	}
}

var _ = context.Background // silence unused import in tests that don't need it
var _ = chi.NewRouter
```

- [ ] **Step 2: Implement Manager.StoreFor (small convenience accessor)**

Add to `internal/session/manager.go`:

```go
// StoreFor returns the underlying Store. Used by HTTP handlers that
// need to read/write per-session command JSONs directly.
func (m *Manager) StoreFor() *store.Store {
	return m.cfg.Store
}
```

- [ ] **Step 3: Replace commands.go**

Replace `internal/api/commands.go` entirely:

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// ListCommandsHandler: GET /api/sessions/{sid}/commands?limit=N&before=ID
//
// Returns metadata only (no output bodies). Empty list is `[]`. An
// unknown session returns `[]` rather than 404 — the frontend treats
// "no session" and "session with no commands" the same; this is also
// kinder to race conditions during cross-tab deletes.
func ListCommandsHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		before := r.URL.Query().Get("before")
		list, err := m.StoreFor().List(sid, limit, before)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if list == nil {
			list = []store.Record{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})
}

type fullRecord struct {
	store.Record
	Output string `json:"output"`
}

// GetCommandHandler: GET /api/sessions/{sid}/commands/{id}
func GetCommandHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		id := chi.URLParam(r, "id")
		rec, err := m.StoreFor().Get(sid, id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such command")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		out, err := m.StoreFor().ReadOutput(sid, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fullRecord{Record: rec, Output: string(out)})
	})
}

// StopCommandHandler: POST /api/sessions/{sid}/commands/{id}/stop
// Returns 204 if id is currently running in sid; 409 otherwise;
// 404 if sid is unknown.
func StopCommandHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		id := chi.URLParam(r, "id")
		sh, err := m.Get(sid)
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "manager_error", err.Error())
			return
		}
		cur := sh.CurrentCommand()
		if cur == nil || cur.ID != id {
			writeError(w, http.StatusConflict, "not_running", "command is not currently running")
			return
		}
		// Stamp status=stopped BEFORE issuing the SIGKILL so the Ended
		// event handler in ws.go sees it and does not promote to
		// "completed". This is the only place that writes StatusStopped.
		if rec, err := m.StoreFor().Get(sid, id); err == nil {
			rec.Status = store.StatusStopped
			_ = m.StoreFor().Save(sid, rec)
		}
		sh.Stop()
		w.WriteHeader(http.StatusNoContent)
	})
}
```

- [ ] **Step 4: Run, confirm green**

Run: `go test ./internal/api/ -run "Test(List|Get|Stop)Command" -count=1 -v`
Expected: 6 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/commands.go internal/api/commands_test.go internal/session/manager.go
git commit -m "api: commands handlers now session-scoped (/api/sessions/{sid}/commands*)"
```

---

## Task 4: Wire up the router

The Deps struct changes shape: drop `Shell` + `Store`, add `Manager`.
The router mounts both the session CRUD and the moved-under-sessions
commands. The WS route stays on `/ws`; Plan 6 will rewrite its
handler. For this plan we let `WSHandler` continue to take its old
signature — main.go can keep passing the legacy fields temporarily,
or just be left broken until Plan 7 fixes boot wiring. We choose
**leave broken**: simpler, and Plan 7 fixes it.

**Files:**
- Modify: `internal/api/router.go`

- [ ] **Step 1: Replace router.go**

Replace `internal/api/router.go`:

```go
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/auth"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/static"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// Deps is the runtime dependency bundle. Plan 7 fills these in main.go.
type Deps struct {
	Manager     *session.Manager
	Auth        auth.Auth
	RateLimiter *auth.RateLimiter
	Ready       func() bool

	// Shell and Store are retained so the legacy WS handler still
	// compiles in this plan. Plan 6 rewrites ws.go to use Manager
	// and drops these fields.
	Shell *shell.Shell
	Store *store.Store
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RecoverMiddleware())
	r.Use(RequestLogger())

	r.Get("/healthz", HealthzHandler().ServeHTTP)
	r.Get("/readyz", ReadyzHandler(d.Ready).ServeHTTP)
	r.Post("/api/login", LoginHandler(d.Auth, d.RateLimiter).ServeHTTP)
	r.Get("/ws", WSHandler(d.Shell, d.Store, d.Auth).ServeHTTP)

	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(d.Auth))

		// Session CRUD.
		r.Get("/api/sessions", ListSessionsHandler(d.Manager).ServeHTTP)
		r.Post("/api/sessions", CreateSessionHandler(d.Manager).ServeHTTP)
		r.Patch("/api/sessions/{id}", RenameSessionHandler(d.Manager).ServeHTTP)
		r.Delete("/api/sessions/{id}", DeleteSessionHandler(d.Manager).ServeHTTP)

		// Session-scoped commands.
		r.Get("/api/sessions/{sid}/commands", ListCommandsHandler(d.Manager).ServeHTTP)
		r.Get("/api/sessions/{sid}/commands/{id}", GetCommandHandler(d.Manager).ServeHTTP)
		r.Post("/api/sessions/{sid}/commands/{id}/stop", StopCommandHandler(d.Manager).ServeHTTP)
	})

	r.NotFound(static.Handler().ServeHTTP)
	return r
}
```

- [ ] **Step 2: Confirm the api package compiles**

Run: `go build ./internal/api/`
Expected: PASS. The old `commands.go` is gone; the new `commands.go` references Manager. `ws.go` still references the old `shell.Shell` — that's fine, it's still there.

- [ ] **Step 3: Run all api unit tests**

Run: `go test ./internal/api/ -count=1`
Expected: PASS for session and commands tests. Existing WS tests may pass since ws.go isn't touched.

- [ ] **Step 4: Commit**

```bash
git add internal/api/router.go
git commit -m "api: router mounts /api/sessions* + moved /api/sessions/{sid}/commands*"
```

---

## Plan 5 acceptance

- `go test -race ./internal/api/ -run "TestSession|TestListSessions|TestCreateSession|Test(Rename|Delete|List|Get|Stop)Session|Test(List|Get|Stop)Command"` is green.
- New routes mounted: `GET/POST /api/sessions`, `PATCH/DELETE /api/sessions/{id}`, `GET /api/sessions/{sid}/commands`, `GET /api/sessions/{sid}/commands/{id}`, `POST /api/sessions/{sid}/commands/{id}/stop`.
- Old `/api/commands*` routes are gone.
- WS handler is unchanged here; Plan 6 rewires it. Boot wiring (Plan 7) will swap `Deps.Shell` / `Deps.Store` for `Deps.Manager` cleanly.

---

## Plan 5 self-review checklist

- [ ] No `TODO|FIXME|XXX` in `internal/api/sessions.go` or the new `commands.go`.
- [ ] Every handler returns the documented status code (verified by tests).
- [ ] `m.StoreFor()` is the only new method added to Manager; everything else uses existing methods.
- [ ] Empty arrays serialize as `[]` not `null` (asserted in `EmptyReturnsEmptyArray` and `EmptyReturnsEmptyArrayOnUnknownSession`).
