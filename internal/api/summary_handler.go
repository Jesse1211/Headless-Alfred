package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/summary"
)

// GetSummaryHandler serves the current contents of the session's
// summary file. 200 with the body on success (empty body is fine
// — the frontend renders the "no summary yet" state for both 404
// and empty 200). 404 when the file doesn't exist.
//
// Path traversal is bounded by enforcing that the resolved path
// stays under <dataDir>/summaries/. Any attempt to escape returns
// 404 (we never let an Open touch an unrelated path).
func GetSummaryHandler(dataDir string) http.Handler {
	root := summary.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		// Defence in depth: refuse anything with separators or
		// `..` segments in the sid. Real session IDs are ULIDs —
		// they never legitimately contain these characters.
		if strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
			writeError(w, http.StatusNotFound, "not_found", "no such summary")
			return
		}
		path := summary.Path(dataDir, sid)
		// Confirm the resolved abs path is still under root.
		clean := filepath.Clean(path)
		if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
			writeError(w, http.StatusNotFound, "not_found", "no such summary")
			return
		}
		body, err := os.ReadFile(clean)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "not_found", "no summary file")
				return
			}
			writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(body)
	})
}
