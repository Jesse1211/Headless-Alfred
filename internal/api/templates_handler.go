package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/template"
)

// GetTemplateHandler serves the raw, un-substituted template
// content so the frontend can show the user what gets injected.
// Read-only — there is no PUT/POST counterpart.
func GetTemplateHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		tpl, ok := template.Builtins[id]
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such template")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(tpl.Content))
	})
}
