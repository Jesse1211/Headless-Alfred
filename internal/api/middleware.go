// Package api provides the HTTP/WebSocket handlers, middleware, and router
// that bind shell, store, and auth into a runnable web service.
package api

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jesseliu/headless-alfred/internal/auth"
)

// errBody is the shape returned for every 4xx/5xx HTTP response.
type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes a JSON error response. It is the canonical way to fail
// an HTTP request from anywhere in this package.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errBody{Code: code, Message: msg})
}

// AuthMiddleware enforces `Authorization: Bearer <token>` on a handler.
// Wraps the request to add nothing else; the token has no claims, just
// equality.
func AuthMiddleware(a auth.Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
				return
			}
			tok := strings.TrimPrefix(h, prefix)
			if !a.VerifyToken(tok) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RecoverMiddleware turns any panic in the downstream handler into a 500
// response. The stack is logged at error level.
func RecoverMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					slog.Error("panic recovered",
						"path", r.URL.Path,
						"panic", rv,
						"stack", string(debug.Stack()),
					)
					writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger logs each request with method, path, status, duration, and
// source IP. Body, query string, and headers are deliberately NOT logged —
// they may contain tokens or command text the user wouldn't want disclosed.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)
			slog.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"dur_ms", time.Since(start).Milliseconds(),
				"ip", ClientIP(r),
			)
		})
	}
}

// statusRecorder lets RequestLogger see the response status without
// re-implementing http.ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.status = c
	s.ResponseWriter.WriteHeader(c)
}

// ClientIP returns the IP of the original client.
//
// In this deployment, Traefik terminates TLS in front of the Go server and
// is the only entity that can set X-Forwarded-For (because the Service is
// not directly reachable from outside the cluster). So we trust the LEFTMOST
// XFF hop as the real client. If no XFF header is present, fall back to
// RemoteAddr (host portion only).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
