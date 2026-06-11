package api

import (
	"encoding/json"
	"net/http"

	"github.com/jesseliu/headless-alfred/internal/auth"
)

type loginReq struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

type loginResp struct {
	Token string `json:"token"`
}

// LoginHandler returns a handler for POST /api/login.
//
// Flow:
//  1. Rate-limit check by source IP. Exhausted → 429.
//  2. Parse body (max 1 KB). Malformed → 400.
//  3. Constant-time credential compare via auth. Wrong → 401.
//  4. Success → return the static token in the JSON body.
//
// Note: the rate-limit check runs BEFORE body parse so that a malformed POST
// still consumes a token; otherwise an attacker could probe credentials
// rapidly by sending invalid JSON.
func LoginHandler(a auth.Auth, rl *auth.RateLimiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !rl.Allow(ip) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts; try again later")
			return
		}
		var req loginReq
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
			return
		}
		tok, ok := a.CheckLogin(req.User, req.Password)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "wrong username or password")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loginResp{Token: tok})
	})
}
