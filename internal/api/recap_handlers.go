package api

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/recap"
	"github.com/jesseliu/headless-alfred/internal/session"
	"github.com/jesseliu/headless-alfred/internal/store"
)

// recapDateParam matches strict YYYY-MM-DD path parameters (no .md suffix).
var recapDateParam = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

type recapEntry struct {
	Date    string `json:"date"`
	IsToday bool   `json:"isToday"`
}

// ListRecapsHandler — GET /api/recaps
// Returns dates that have a recap file, newest first.
func ListRecapsHandler(dataDir string) http.Handler {
	dir := recap.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusOK, []recapEntry{})
				return
			}
			writeError(w, http.StatusInternalServerError, "io_error", err.Error())
			return
		}
		var dates []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			base := strings.TrimSuffix(name, ".md")
			if !recapDateParam.MatchString(base) {
				continue
			}
			dates = append(dates, base)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dates))) // YYYY-MM-DD sorts lexicographically == chronologically
		td := time.Now().Local().Format("2006-01-02")
		out := make([]recapEntry, len(dates))
		for i, d := range dates {
			out[i] = recapEntry{Date: d, IsToday: d == td}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// GetRecapHandler — GET /api/recaps/{date}
func GetRecapHandler(dataDir string) http.Handler {
	root := recap.Dir(dataDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := chi.URLParam(r, "date")
		if !recapDateParam.MatchString(date) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid date")
			return
		}
		serveMarkdownFile(w, root, date+".md", "no recap for that date")
	})
}

// CreateRecapSessionHandler — POST /api/recap-sessions
// Find or create the singleton recap session.
func CreateRecapSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, err := m.CreateOrGetRecapSession()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "recap_create_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, meta)
	})
}

// DeleteRecapSessionHandler — DELETE /api/recap-sessions/current
// Idempotent: 204 even if no recap session exists.
func DeleteRecapSessionHandler(m *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recaps := m.List(store.KindRecap)
		for _, rec := range recaps {
			if err := m.Close(rec.ID); err != nil {
				// Log on header; continue closing others.
				w.Header().Set("X-Recap-Close-Warning", err.Error())
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
