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

var _ = context.Background // silence unused import
var _ = chi.NewRouter
