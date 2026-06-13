package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// AnthropicCredentialsHandler: POST /api/anthropic-credentials
//
// Body: the raw contents of the user's `~/.claude/.credentials.json` —
// the file the `claude` CLI writes after a successful OAuth login.
// Shape (top level):
//
//	{
//	  "claudeAiOauth": {
//	    "accessToken":  "sk-ant-oat01-...",
//	    "refreshToken": "sk-ant-ort01-...",
//	    "expiresAt":    9999999999000,
//	    "scopes":       ["user:inference", ...]
//	  }
//	}
//
// We don't decode for any other purpose than minimal shape validation —
// the field names and types are owned by Anthropic, not us, and may
// gain fields over time. We just check that the JSON is well-formed
// AND contains a non-empty accessToken under claudeAiOauth.
//
// Where it lands: ~/.claude/.credentials.json (mode 0600). This is the
// ONE path claude 2.1.x in the container actually reads — verified by
// probing every plausible alternative path (.claude/credentials.json,
// .config/claude/, .config/anthropic/claude-code/) and observing that
// only the leading-dot file under ~/.claude/ produces a "401 Invalid
// bearer token" response (= claude tried to use it).
//
// Responses:
//
//	204 on success
//	400 bad_request — malformed JSON
//	422 bad_field — missing claudeAiOauth.accessToken
//	413 too_large — body > 64 KiB
//	500 write_failed — file IO error
//
// The token is NEVER logged. RequestLogger only records method + path.
func AnthropicCredentialsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const maxBodyBytes = 64 * 1024
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "credentials body exceeds 64 KiB")
			return
		}
		if len(body) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "empty body")
			return
		}

		// Minimal shape validation: parse just enough to confirm an
		// accessToken is present. We don't enforce other fields because
		// Anthropic owns this schema and may evolve it; the cost of
		// being too strict is high (locks users out on a version bump),
		// the cost of being too lax is low (file is invalid → next
		// claude call returns 401 → user re-uploads).
		var probe struct {
			ClaudeAiOauth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON")
			return
		}
		if probe.ClaudeAiOauth.AccessToken == "" {
			writeError(w, http.StatusUnprocessableEntity, "bad_field",
				"claudeAiOauth.accessToken required (paste your full ~/.claude/.credentials.json)")
			return
		}

		home, err := os.UserHomeDir()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", "no home dir")
			return
		}
		dir := filepath.Join(home, ".claude")
		if err := os.MkdirAll(dir, 0700); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed",
				fmt.Sprintf("mkdir %s: %v", dir, err))
			return
		}
		// Atomic write: tmp + rename, so a concurrent claude that's
		// reading the file doesn't see half-written contents.
		credPath := filepath.Join(dir, ".credentials.json")
		tmp, err := os.CreateTemp(dir, ".credentials.json.tmp.*")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed",
				fmt.Sprintf("create tmp: %v", err))
			return
		}
		tmpName := tmp.Name()
		// Best-effort cleanup if anything below fails.
		defer os.Remove(tmpName)
		if _, err := tmp.Write(body); err != nil {
			tmp.Close()
			writeError(w, http.StatusInternalServerError, "write_failed",
				fmt.Sprintf("write tmp: %v", err))
			return
		}
		if err := tmp.Chmod(0600); err != nil {
			tmp.Close()
			writeError(w, http.StatusInternalServerError, "write_failed",
				fmt.Sprintf("chmod tmp: %v", err))
			return
		}
		if err := tmp.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed",
				fmt.Sprintf("close tmp: %v", err))
			return
		}
		if err := os.Rename(tmpName, credPath); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed",
				fmt.Sprintf("rename: %v", err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
