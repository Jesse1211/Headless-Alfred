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
)

func TestListRecaps_EmptyDirReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Get("/api/recaps", ListRecapsHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps", nil))
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q (want [])", w.Body.String())
	}
}

func TestListRecaps_ReturnsDatesDesc(t *testing.T) {
	dir := t.TempDir()
	rdir := filepath.Join(dir, "recaps")
	_ = os.MkdirAll(rdir, 0o755)
	for _, d := range []string{"2026-06-10", "2026-06-12", "2026-06-15"} {
		_ = os.WriteFile(filepath.Join(rdir, d+".md"), []byte("x"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(rdir, "hello.md"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(rdir, "2026-6-15.md"), []byte("x"), 0o644)

	r := chi.NewRouter()
	r.Get("/api/recaps", ListRecapsHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps", nil))

	var got []struct {
		Date string `json:"date"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3 (body=%s)", len(got), w.Body.String())
	}
	wantOrder := []string{"2026-06-15", "2026-06-12", "2026-06-10"}
	for i, want := range wantOrder {
		if got[i].Date != want {
			t.Errorf("position %d: got %q want %q", i, got[i].Date, want)
		}
	}
}

func TestGetRecap_Returns200WithMarkdown(t *testing.T) {
	dir := t.TempDir()
	rdir := filepath.Join(dir, "recaps")
	_ = os.MkdirAll(rdir, 0o755)
	body := "# Recap · 2026-06-15\n\n## Shipped\n- thing\n"
	_ = os.WriteFile(filepath.Join(rdir, "2026-06-15.md"), []byte(body), 0o644)

	r := chi.NewRouter()
	r.Get("/api/recaps/{date}", GetRecapHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps/2026-06-15", nil))
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != body {
		t.Errorf("body mismatch:\n got %q\nwant %q", w.Body.String(), body)
	}
}

func TestGetRecap_Returns404ForMissingDate(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "recaps"), 0o755)

	r := chi.NewRouter()
	r.Get("/api/recaps/{date}", GetRecapHandler(dir).ServeHTTP)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps/2026-06-15", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

func TestGetRecap_Returns400ForBadDate(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	r.Get("/api/recaps/{date}", GetRecapHandler(dir).ServeHTTP)
	for _, bad := range []string{"2026-6-15", "tomorrow"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/recaps/"+bad, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("date %q: status %d, want 400", bad, w.Code)
		}
	}
}
