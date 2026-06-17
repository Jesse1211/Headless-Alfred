package api

import (
	"net/http"
	"sort"

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

// templateSummary is the per-template entry returned by
// ListTemplatesHandler. We deliberately do NOT ship Content here —
// the composer's checkbox list only needs id + name to render.
// Clients that want the body can hit GET /api/templates/{id}.
type templateSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListTemplatesHandler returns every registered template's id +
// name in a stable (alphabetical) order. The composer renders these
// as checkboxes so the user can pick per-prompt which to inject.
//
// We skip "recap-daily" because it's not user-pickable per prompt —
// it's only fired by the Recap "Generate" button via the
// renderTemplate path. Listing it in the composer would confuse
// users into thinking they can attach the recap template to a
// chat session.
func ListTemplatesHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stable order across calls so the checkbox list doesn't
		// reshuffle between renders.
		ids := make([]string, 0, len(template.Builtins))
		for id := range template.Builtins {
			if id == "recap-daily" {
				continue
			}
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := make([]templateSummary, 0, len(ids))
		for _, id := range ids {
			out = append(out, templateSummary{ID: id, Name: template.Builtins[id].Name})
		}
		writeJSON(w, http.StatusOK, out)
	})
}
