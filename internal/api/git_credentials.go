package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// GitCredentialsHandler: POST /api/git-credentials
// Body: { "host": "github.com", "username": "<user>", "token": "<pat>" }
//
// Writes ~/.git-credentials with one line for the given host, plus
// ~/.gitconfig with credential.helper=store, so subsequent
// `git clone/pull/push https://host/...` invocations authenticate
// automatically without showing the token in the user's command history.
//
// Responses:
//
//	204 on success
//	400 bad_request — malformed JSON
//	422 bad_field — missing host/username/token, or host fails URL-host validation
//	500 write_failed — file IO error
//
// The token is NEVER logged. Request body is small (<8 KiB) and not
// captured by middleware.RequestLogger (which only logs method + path).
func GitCredentialsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Host     string `json:"host"`
			Username string `json:"username"`
			Token    string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
			return
		}
		req.Host = strings.TrimSpace(req.Host)
		req.Username = strings.TrimSpace(req.Username)
		req.Token = strings.TrimSpace(req.Token)
		if req.Host == "" {
			writeError(w, http.StatusUnprocessableEntity, "bad_field", "host required")
			return
		}
		// host must be a bare hostname like github.com, not a URL. Reject
		// anything with a scheme or slashes; that's a sign the caller is
		// pasting a clone URL by mistake.
		if strings.ContainsAny(req.Host, "/:\\ ") {
			writeError(w, http.StatusUnprocessableEntity, "bad_field", "host must be a bare hostname (e.g. github.com)")
			return
		}
		if req.Username == "" {
			writeError(w, http.StatusUnprocessableEntity, "bad_field", "username required")
			return
		}
		if req.Token == "" {
			writeError(w, http.StatusUnprocessableEntity, "bad_field", "token required")
			return
		}

		home, err := os.UserHomeDir()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", "no home dir")
			return
		}
		if err := writeGitCredentials(home, req.Host, req.Username, req.Token); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		if err := ensureCredentialHelperStore(home); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// writeGitCredentials writes (or replaces) the line for host inside
// ~/.git-credentials. Format is one URL per line:
//
//	https://username:token@host
//
// Other hosts' lines are preserved. File mode 0600 so only the alfred
// user can read it.
func writeGitCredentials(home, host, username, token string) error {
	credPath := filepath.Join(home, ".git-credentials")
	existing, err := os.ReadFile(credPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing credentials: %w", err)
	}
	// Build a new credential URL. url.QueryEscape would over-escape the
	// username (e.g. encoding '@') — git accepts un-escaped here, but
	// any literal ':' or '@' in the user breaks the format. PATs and
	// usual GitHub-style usernames don't hit this, so we keep it simple
	// and reject obviously bad chars.
	if strings.ContainsAny(username, ":@/\n") || strings.ContainsAny(token, "\n") {
		return fmt.Errorf("username/token contains illegal characters")
	}
	newLine := fmt.Sprintf("https://%s:%s@%s",
		url.PathEscape(username), url.PathEscape(token), host)

	// Keep other-host lines, replace any line ending in @host.
	var keep []string
	hostSuffix := "@" + host
	for _, line := range strings.Split(string(existing), "\n") {
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, hostSuffix) {
			continue
		}
		keep = append(keep, line)
	}
	keep = append(keep, newLine)
	body := strings.Join(keep, "\n") + "\n"
	if err := os.WriteFile(credPath, []byte(body), 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

// ensureCredentialHelperStore makes sure ~/.gitconfig has
// `credential.helper = store`. We don't shell out to `git config` —
// the file is INI-ish and we only need this one knob. If the file
// exists and already references the store helper we leave it alone;
// otherwise we append a [credential] section.
func ensureCredentialHelperStore(home string) error {
	cfgPath := filepath.Join(home, ".gitconfig")
	existing, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read gitconfig: %w", err)
	}
	if strings.Contains(string(existing), "helper = store") ||
		strings.Contains(string(existing), "helper=store") {
		return nil
	}
	appended := string(existing)
	if appended != "" && !strings.HasSuffix(appended, "\n") {
		appended += "\n"
	}
	appended += "[credential]\n\thelper = store\n"
	if err := os.WriteFile(cfgPath, []byte(appended), 0600); err != nil {
		return fmt.Errorf("write gitconfig: %w", err)
	}
	return nil
}
