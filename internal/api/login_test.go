package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jesseliu/headless-alfred/internal/auth"
)

func TestLogin_Success(t *testing.T) {
	a := auth.Auth{User: "admin", Password: "pw", Token: "TOK"}
	rl := auth.NewRateLimiter(10, time.Minute)
	h := LoginHandler(a, rl)
	body := bytes.NewBufferString(`{"user":"admin","password":"pw"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct{ Token string }
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != "TOK" {
		t.Fatalf("token = %q", resp.Token)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	a := auth.Auth{User: "admin", Password: "pw", Token: "TOK"}
	rl := auth.NewRateLimiter(10, time.Minute)
	h := LoginHandler(a, rl)
	body := bytes.NewBufferString(`{"user":"admin","password":"WRONG"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestLogin_RateLimited(t *testing.T) {
	a := auth.Auth{User: "admin", Password: "pw", Token: "TOK"}
	rl := auth.NewRateLimiter(2, time.Minute)
	h := LoginHandler(a, rl)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"user":"x","password":"y"}`))
		req.RemoteAddr = "1.1.1.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
	// 3rd attempt should be 429.
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"user":"x","password":"y"}`))
	req.RemoteAddr = "1.1.1.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rec.Code)
	}
}

func TestLogin_MalformedJSON(t *testing.T) {
	a := auth.Auth{User: "admin", Password: "pw", Token: "TOK"}
	rl := auth.NewRateLimiter(10, time.Minute)
	h := LoginHandler(a, rl)
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}
