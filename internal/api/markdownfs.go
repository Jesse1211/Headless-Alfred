package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// serveMarkdownFile writes <root>/<basename> as text/markdown, with
// the prefix-on-resolved-path defence against traversal.
//
// Caller is responsible for validating the BASENAME's shape (sid
// character set, date pattern, etc.) — this helper only re-checks
// that the resolved path stays under root after filepath.Clean. If a
// traversal slips through (e.g. caller forgot to validate sid), the
// resolved path will escape and we return 404 instead of opening a
// file outside the directory.
//
// notFoundMessage is the human-readable reason returned on either
// the traversal block or os.ErrNotExist — both fail with 404 so a
// caller can't distinguish "didn't exist" from "tried to escape".
func serveMarkdownFile(w http.ResponseWriter, root, basename, notFoundMessage string) {
	clean := filepath.Clean(filepath.Join(root, basename))
	if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
		writeError(w, http.StatusNotFound, "not_found", notFoundMessage)
		return
	}
	body, err := os.ReadFile(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", notFoundMessage)
			return
		}
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(body)
}
