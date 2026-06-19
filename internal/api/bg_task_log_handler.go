package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudebgtasks"
)

const (
	bgTaskLogDefaultTail = 8192
	bgTaskLogMaxTail     = 65536
)

// taskIDRe is the validation pattern for bg-task IDs: alphanumeric, underscore,
// hyphen, 1–20 chars.
var taskIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,20}$`)

// CWDResolver resolves the realpath-cwd for an Alfred session. The cwd is
// determined at run-time by tmux pane_current_path (via session.Manager.Get
// + shell.CurrentCWD + filepath.EvalSymlinks), not from static metadata, so
// it lives in a separate interface from MetaResolver.
type CWDResolver interface {
	CWDFor(sessionID string) (string, error)
}

// ErrCWDUnknown is returned by CWDResolver when the session's cwd cannot
// be determined (shell not started, pane dead, etc.).
var ErrCWDUnknown = errors.New("api: cwd unknown for session")

// bgTaskLogUnavailable writes a JSON 200 response signalling the log cannot
// be served, with a reason string for the frontend.
func bgTaskLogUnavailable(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "log_unavailable",
		"reason": reason,
	})
}

// GetBgTaskLogHandler serves the tail of a bg-task's stdout file.
//
// Query: ?tail=N (default 8192, max 65536). Returns JSON
//
//	{ "bytes": "<base64>", "size": <int>, "truncated": <bool> }
//
// on success, or
//
//	{ "status": "log_unavailable", "reason": "<short>" }
//
// if the file cannot be found / read. Both cases are HTTP 200; the frontend
// distinguishes by the "status" field.
//
// taskID is validated against ^[a-zA-Z0-9_-]{1,20}$; invalid IDs → 400.
func GetBgTaskLogHandler(meta MetaResolver, cwdRes CWDResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "sid")
		if sid == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "missing sid")
			return
		}
		taskID := chi.URLParam(r, "taskId")
		if !taskIDRe.MatchString(taskID) {
			writeError(w, http.StatusBadRequest, "invalid_task_id", "taskId must match ^[a-zA-Z0-9_-]{1,20}$")
			return
		}

		// Parse ?tail=N.
		tail := bgTaskLogDefaultTail
		if raw := r.URL.Query().Get("tail"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				tail = n
			}
			// On parse error or n <= 0: keep default (spec: treat as default).
		}
		if tail > bgTaskLogMaxTail {
			tail = bgTaskLogMaxTail
		}

		// Resolve Claude session UUID from Alfred session ID.
		sessionUUID, err := meta.ClaudeUUIDFor(sid)
		if errors.Is(err, ErrUnknownSession) {
			writeError(w, http.StatusNotFound, "unknown_session", "no such session")
			return
		}
		if err != nil {
			slog.Error("bg-task-log: meta resolve", "sid", sid, "err", err)
			writeError(w, http.StatusInternalServerError, "meta_error", "resolve failed")
			return
		}
		if sessionUUID == "" {
			// Session exists but has never entered Claude mode → no log.
			bgTaskLogUnavailable(w, "file_not_found")
			return
		}

		// Resolve realpath-cwd for the session.
		realpathCwd, err := cwdRes.CWDFor(sid)
		if errors.Is(err, ErrCWDUnknown) || realpathCwd == "" {
			bgTaskLogUnavailable(w, "cwd_unknown")
			return
		}
		if err != nil {
			slog.Error("bg-task-log: cwd resolve", "sid", sid, "err", err)
			bgTaskLogUnavailable(w, "cwd_unknown")
			return
		}

		// Compute the output file path via the canonical helper (T6).
		logPath := claudebgtasks.OutputPath(realpathCwd, sessionUUID, taskID)

		// Stat the file; missing → log_unavailable.
		info, err := os.Stat(logPath)
		if errors.Is(err, os.ErrNotExist) {
			bgTaskLogUnavailable(w, "file_not_found")
			return
		}
		if err != nil {
			slog.Error("bg-task-log: stat", "path", logPath, "err", err)
			bgTaskLogUnavailable(w, "file_not_found")
			return
		}

		fileSize := int(info.Size())
		truncated := fileSize > tail

		// Open and seek to (size - tail) clamped at 0, then read tail bytes.
		f, err := os.Open(logPath)
		if err != nil {
			slog.Error("bg-task-log: open", "path", logPath, "err", err)
			bgTaskLogUnavailable(w, "file_not_found")
			return
		}
		defer f.Close()

		readN := tail
		if fileSize < tail {
			readN = fileSize
		}
		seekOff := int64(fileSize - readN)
		if _, err := f.Seek(seekOff, io.SeekStart); err != nil {
			slog.Error("bg-task-log: seek", "path", logPath, "err", err)
			bgTaskLogUnavailable(w, "file_not_found")
			return
		}

		buf := make([]byte, readN)
		if _, err := io.ReadFull(f, buf); err != nil && !errors.Is(err, io.EOF) {
			slog.Error("bg-task-log: read", "path", logPath, "err", err)
			bgTaskLogUnavailable(w, "file_not_found")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Bytes     string `json:"bytes"`
			Size      int    `json:"size"`
			Truncated bool   `json:"truncated"`
		}{
			Bytes:     base64.StdEncoding.EncodeToString(buf),
			Size:      fileSize,
			Truncated: truncated,
		})
	})
}
