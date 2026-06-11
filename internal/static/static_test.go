package static

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler_404ForNonExistentFileWhenNoIndex(t *testing.T) {
	// Pre-frontend: dist/ contains only .gitkeep. There's no index.html,
	// so any request that's not for an existing file must 404 (no SPA
	// fallback possible).
	h := Handler()
	req := httptest.NewRequest("GET", "/nonexistent-file.xyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (no index.html to fall back to)", rec.Code)
	}
}

func TestHandler_MethodNotAllowedForPost(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("POST", "/anything", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", rec.Code)
	}
}
