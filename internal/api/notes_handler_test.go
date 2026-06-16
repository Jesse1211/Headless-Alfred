package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestGetNote_Missing_Returns404(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/note", GetNoteHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/sid-A/note", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

func TestGetNote_Existing_ReturnsBody(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
	body := "remember to test the recap edge case"
	_ = os.WriteFile(filepath.Join(dir, "notes", "sid-A.md"), []byte(body), 0o644)

	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/note", GetNoteHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/sid-A/note", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != body {
		t.Errorf("body = %q, want %q", w.Body.String(), body)
	}
}

func TestPutNote_WritesBody(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Put("/api/sessions/{sid}/note", PutNoteHandler(dir).ServeHTTP)
	body := "first note"
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/sessions/sid-A/note", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/markdown")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "notes", "sid-A.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("got %q want %q", string(got), body)
	}
}

func TestPutNote_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Put("/api/sessions/{sid}/note", PutNoteHandler(dir).ServeHTTP)
	huge := strings.Repeat("x", 64*1024+1)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/sessions/sid-A/note", strings.NewReader(huge))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 413 or 400", w.Code)
	}
}

func TestPutNote_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Put("/api/sessions/{sid}/note", PutNoteHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/sessions/..%2Fhacked/note", strings.NewReader("x"))
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNoContent {
		t.Errorf("traversal must NOT succeed; got 204")
	}
	if _, err := os.Stat(filepath.Join(dir, "hacked")); err == nil {
		t.Errorf("file written outside notes/")
	}
}
