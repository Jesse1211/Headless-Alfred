package api

import (
	"net/http"

	"github.com/jesseliu/headless-alfred/internal/shell"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// Placeholder during P5T1/2: real session-scoped implementation lands
// in P5T3. Plan 7 will rewire the router not to call these.
var _ = store.ErrNotFound
var _ shell.RunningCommand

func ListCommandsHandler(_ *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ListCommandsHandler not implemented yet (Plan 5 Task 3)", http.StatusInternalServerError)
	})
}

func GetCommandHandler(_ *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "GetCommandHandler not implemented yet (Plan 5 Task 3)", http.StatusInternalServerError)
	})
}

func StopCommandHandler(_ *shell.Shell) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "StopCommandHandler not implemented yet (Plan 5 Task 3)", http.StatusInternalServerError)
	})
}
