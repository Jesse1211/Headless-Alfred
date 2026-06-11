package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jesseliu/headless-alfred/internal/auth"
)

func TestAuthMiddleware_AcceptsValidBearer(t *testing.T) {
	a := auth.Auth{Token: "TOK"}
	called := false
	h := AuthMiddleware(a)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer TOK")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("inner handler not called")
	}
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAuthMiddleware_RejectsMissingHeader(t *testing.T) {
	a := auth.Auth{Token: "TOK"}
	h := AuthMiddleware(a)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_RejectsWrongToken(t *testing.T) {
	a := auth.Auth{Token: "TOK"}
	h := AuthMiddleware(a)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer WRONG")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestRecoverMiddleware_TurnsPanicInto500(t *testing.T) {
	h := RecoverMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
}

func TestRequestLogger_PreservesHijacker(t *testing.T) {
	// Regression: RequestLogger wraps the ResponseWriter in a statusRecorder.
	// If statusRecorder doesn't expose http.Hijacker, WebSocket upgrades fail
	// with "response does not implement http.Hijacker".
	got := false
	h := RequestLogger()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, isHijacker := w.(http.Hijacker)
		got = isHijacker
	}))
	// httptest.NewRecorder is NOT a Hijacker, so we need a real http.Server's
	// ResponseWriter via httptest.NewServer.
	srv := httptest.NewServer(h)
	defer srv.Close()
	_, _ = http.Get(srv.URL)
	if !got {
		t.Fatal("inner handler's ResponseWriter does not implement http.Hijacker; WebSocket upgrades will fail")
	}
}

func TestClientIP_PrefersXForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.5")
	got := ClientIP(req)
	if got != "203.0.113.7" {
		t.Fatalf("got %q, want 203.0.113.7", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	got := ClientIP(req)
	if got != "10.0.0.1" {
		t.Fatalf("got %q, want 10.0.0.1", got)
	}
}
