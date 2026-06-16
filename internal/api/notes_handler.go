package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/notes"
)

// maxNoteBytes caps PUT payloads. Notes are short user scratchpad,
// not log dumps — 64KB is generous.
const maxNoteBytes = 64 * 1024

// GetNoteHandler serves the notes body for the session. 200 + body
// on success, 404 when the file doesn't exist (frontend renders the
// empty state).
func GetNoteHandler(dataDir string) http.Handler {
	root := notes.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
			writeError(w, http.StatusNotFound, "not_found", "no such note")
			return
		}
		path := notes.Path(dataDir, sid)
		clean := filepath.Clean(path)
		if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
			writeError(w, http.StatusNotFound, "not_found", "no such note")
			return
		}
		body, err := os.ReadFile(clean)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "not_found", "no note file")
				return
			}
			writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(body)
	})
}

// PutNoteHandler writes the request body to the session's notes path
// atomically (tmp + rename), capped at maxNoteBytes. 204 on success.
func PutNoteHandler(dataDir string) http.Handler {
	root := notes.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid session id")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNoteBytes+1))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
			return
		}
		if len(body) > maxNoteBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "note exceeds 64KB cap")
			return
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			writeError(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
			return
		}
		final := notes.Path(dataDir, sid)
		clean := filepath.Clean(final)
		if !strings.HasPrefix(clean, filepath.Clean(root)+string(filepath.Separator)) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid session id")
			return
		}
		tmp := clean + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		if err := os.Rename(tmp, clean); err != nil {
			_ = os.Remove(tmp)
			writeError(w, http.StatusInternalServerError, "rename_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
