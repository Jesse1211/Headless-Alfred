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
