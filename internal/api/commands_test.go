package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jesseliu/headless-alfred/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestListCommands_EmptyReturnsEmptyArray(t *testing.T) {
	s := newTestStore(t)
	h := ListCommandsHandler(s)
	req := httptest.NewRequest("GET", "/api/commands", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if body != "[]\n" && body != "[]" {
		t.Fatalf("body should be empty JSON array, got %q", body)
	}
}

func TestGetCommand_NotFound(t *testing.T) {
	s := newTestStore(t)
	r := chi.NewRouter()
	r.Get("/api/commands/{id}", GetCommandHandler(s).ServeHTTP)
	req := httptest.NewRequest("GET", "/api/commands/missing", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestGetCommand_ReturnsRecord(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(store.Record{ID: "X", Command: "ls", Status: store.StatusCompleted, StartedAt: time.Now()})
	r := chi.NewRouter()
	r.Get("/api/commands/{id}", GetCommandHandler(s).ServeHTTP)
	req := httptest.NewRequest("GET", "/api/commands/X", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID      string `json:"id"`
		Command string `json:"command"`
		Output  string `json:"output"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Command != "ls" {
		t.Fatalf("command = %q", got.Command)
	}
}

func TestGetCommand_IncludesOutputContent(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(store.Record{ID: "Y", Command: "ls", Status: store.StatusCompleted, StartedAt: time.Now()})
	_ = s.WriteOutput("Y", []byte("file1\nfile2\n"))
	r := chi.NewRouter()
	r.Get("/api/commands/{id}", GetCommandHandler(s).ServeHTTP)
	req := httptest.NewRequest("GET", "/api/commands/Y", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var got struct {
		Output string `json:"output"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got.Output != "file1\nfile2\n" {
		t.Fatalf("output = %q", got.Output)
	}
}
