package static

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests run against the embedded dist/ tree. In CI the frontend build
// must have been copied in BEFORE building the Go binary (the Dockerfile
// handles this in production; locally, `cp -R web/dist/. internal/static/dist/`
// before `go test`). If dist/ contains only .gitkeep the SPA-fallback tests
// will be skipped.

func indexExists() bool {
	h := Handler()
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "<html")
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

func TestHandler_SPAFallback_DoesNotRedirect(t *testing.T) {
	// Regression: http.FileServer 301-redirects any path that resolves to
	// /index.html to the parent directory. SPA routes like /login would
	// bounce instead of rendering the app. The fix reads index.html bytes
	// directly without going through FileServer.
	if !indexExists() {
		t.Skip("dist/ has no index.html; build frontend first")
	}
	h := Handler()
	req := httptest.NewRequest("GET", "/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (got body: %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("body should be the SPA index.html, got %q", rec.Body.String())
	}
}

func TestHandler_RealFileServedDirectly(t *testing.T) {
	if !indexExists() {
		t.Skip("dist/ has no index.html; build frontend first")
	}
	h := Handler()
	req := httptest.NewRequest("GET", "/favicon.svg", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// favicon.svg is XML/SVG, definitely not HTML.
	if strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("expected SVG, got SPA fallback HTML")
	}
}
