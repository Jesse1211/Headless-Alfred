package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Mount a router that contains only the template route so the
// {id} URL param resolves through chi the same way it will in
// production.
func mountTemplateRouter(t *testing.T) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/templates/{id}", GetTemplateHandler().ServeHTTP)
	return r
}

func TestGetTemplate_Known_Returns200WithBody(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/templates/summary-todo", nil)
	w := httptest.NewRecorder()
	mountTemplateRouter(t).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "<sid>") || !strings.Contains(string(body), "<summary_path>") {
		t.Errorf("body missing placeholders (we serve the raw, un-substituted template)")
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type=%q, want text/plain; charset=utf-8", got)
	}
}

func TestGetTemplate_Unknown_Returns404(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/templates/does-not-exist", nil)
	w := httptest.NewRecorder()
	mountTemplateRouter(t).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", w.Code)
	}
}
