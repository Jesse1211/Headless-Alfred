package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/summary"
)

// Mount a router with only the summary route so the {sid} URL
// param resolves through chi the same way it will in production.
func mountSummaryRouter(t *testing.T, dataDir string) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/summary", GetSummaryHandler(dataDir).ServeHTTP)
	return r
}

func TestGetSummary_FileExists_Returns200(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(summary.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	body := "## Goal\nbuild a thing"
	if err := os.WriteFile(summary.Path(dir, "S1"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/sessions/S1/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	got, _ := io.ReadAll(w.Result().Body)
	if string(got) != body {
		t.Errorf("body=%q, want %q", got, body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type=%q, want text/markdown", ct)
	}
	if got, want := w.Header().Get("X-File-Path"), summary.Path(dir, "S1"); got != want {
		t.Errorf("X-File-Path=%q, want %q", got, want)
	}
}

func TestGetSummary_FileMissing_Returns404(t *testing.T) {
	dir := t.TempDir()
	req := httptest.NewRequest("GET", "/api/sessions/NOPE/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", w.Code)
	}
	// X-File-Path is emitted even on the not-exist 404 so the UI
	// can show the canonical path while the file doesn't exist yet.
	if got, want := w.Header().Get("X-File-Path"), summary.Path(dir, "NOPE"); got != want {
		t.Errorf("X-File-Path=%q, want %q", got, want)
	}
}

func TestGetSummary_EmptyFile_Returns200WithEmptyBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(summary.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(summary.Path(dir, "S2"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/sessions/S2/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (empty body is the frontend's responsibility)", w.Code)
	}
	got, _ := io.ReadAll(w.Result().Body)
	if len(got) != 0 {
		t.Errorf("body=%q, want empty", got)
	}
}

func TestGetSummary_PathTraversal_Returns404(t *testing.T) {
	dir := t.TempDir()
	// Try to escape the summaries/ dir. chi URLDecodes for us, but
	// even if a traversal sid slipped through, summary.Path uses
	// filepath.Join which would normalise — and our os.Open would
	// hit a nonexistent path on most layouts. We just assert 404.
	req := httptest.NewRequest("GET", "/api/sessions/..%2F..%2Fsessions.json/summary", nil)
	w := httptest.NewRecorder()
	mountSummaryRouter(t, dir).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404 for traversal attempt", w.Code)
	}
	// CRITICAL: traversal rejection MUST NOT leak the resolved path,
	// or we'd be confirming the location of files outside root.
	if got := w.Header().Get("X-File-Path"); got != "" {
		t.Errorf("X-File-Path=%q, must be empty on traversal rejection", got)
	}
}

// Keep filepath import alive even if test doesn't directly use it.
var _ = filepath.Separator
