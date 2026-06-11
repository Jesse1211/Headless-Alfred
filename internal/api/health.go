package api

import "net/http"

// HealthzHandler returns 200 OK if the Go process is alive enough to serve.
// Used by k8s livenessProbe — a failure here triggers a Pod restart.
func HealthzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// ReadyzHandler returns 200 only when the `ready` predicate returns true.
// Used by k8s readinessProbe — failure removes the Pod from Service load
// balancing without restarting it.
func ReadyzHandler(ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}
