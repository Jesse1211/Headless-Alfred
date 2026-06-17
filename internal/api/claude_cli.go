package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
)

// claudeCLIPackage is the npm package we manage. Hardcoded so the
// upgrade handler can't be coerced into installing arbitrary
// packages — version is the only thing the client picks.
const claudeCLIPackage = "@anthropic-ai/claude-code"

// versionAllowed matches what we accept on POST /api/claude-cli/upgrade.
// "latest" / "next" are npm dist-tags; anything else must be a strict
// semver. We deliberately don't allow ranges, git URLs, or local
// paths — those are doors to running arbitrary code.
var versionAllowed = regexp.MustCompile(`^(latest|next|\d+\.\d+\.\d+)$`)

// ClaudeCLIVersionHandler — GET /api/claude-cli/version
// Runs `claude --version`, returns {"version": "2.1.142"}. If claude
// isn't on PATH, returns 500 — but it always should be, because
// either the Dockerfile baked it in or the entrypoint has added
// ~/.npm-global/bin (where user-upgraded versions live) to PATH.
func ClaudeCLIVersionHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := exec.Command("claude", "--version").Output()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "exec_failed", err.Error())
			return
		}
		// CLI prints e.g. "2.1.142 (Claude Code)\n" — first token
		// is the version. Be lenient about trailing garbage so a CLI
		// format change doesn't kill this handler.
		raw := strings.TrimSpace(string(out))
		version := raw
		if i := strings.IndexByte(raw, ' '); i > 0 {
			version = raw[:i]
		}
		writeJSON(w, http.StatusOK, map[string]string{"version": version})
	})
}

type claudeCLIUpgradeReq struct {
	Version string `json:"version"`
}

// ClaudeCLIUpgradeHandler — POST /api/claude-cli/upgrade
// Body: {"version": "latest" | "next" | "X.Y.Z"}
//
// Streams `npm install -g @anthropic-ai/claude-code@<version>`
// stdout+stderr back as chunked text/plain. Frontend appends each
// chunk to a live output area. Final line is "ok: now at <version>"
// on success or "err: ..." on failure so the client has an
// unambiguous tail to key off.
//
// Why chunked text/plain rather than WS:
//   - The operation is one-shot, no bidirectional traffic needed.
//   - npm output IS text; no framing benefit to JSON-wrapping each
//     line.
//   - Avoids piling more handlers onto the WS protocol.
//
// Side effects on success:
//   - The new binary lives at $HOME/.npm-global/bin/claude (because
//     entrypoint set npm prefix there).
//   - alfred-server's NEXT `claude -p` fork picks it up automatically
//     via PATH lookup — no process restart needed.
func ClaudeCLIUpgradeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req claudeCLIUpgradeReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		if !versionAllowed.MatchString(req.Version) {
			writeError(w, http.StatusBadRequest, "bad_version",
				"version must be 'latest', 'next', or X.Y.Z")
			return
		}

		// Flush incremental output as it lands. Without this, our
		// chunks sit in the proxy buffer and the user stares at a
		// blank pane for the whole npm run.
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "no_flusher", "response writer does not support streaming")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		target := claudeCLIPackage + "@" + req.Version
		// CombinedOutput-style: merge stderr into stdout so the
		// stream is sequential. npm prints progress / errors to
		// stderr; the user wants to see them in order with stdout.
		cmd := exec.Command("npm", "install", "-g", "--no-audit", "--no-fund", target)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			_, _ = io.WriteString(w, "err: "+err.Error()+"\n")
			flusher.Flush()
			return
		}
		cmd.Stderr = cmd.Stdout // share the same pipe
		if err := cmd.Start(); err != nil {
			_, _ = io.WriteString(w, "err: "+err.Error()+"\n")
			flusher.Flush()
			return
		}
		_, _ = io.WriteString(w, "$ npm install -g "+target+"\n")
		flusher.Flush()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text() + "\n"
			_, _ = io.WriteString(w, line)
			flusher.Flush()
		}
		waitErr := cmd.Wait()
		if waitErr != nil {
			_, _ = io.WriteString(w, "err: npm exited with "+waitErr.Error()+"\n")
			flusher.Flush()
			return
		}

		// Re-probe version so the client sees what actually got
		// installed (resolves dist-tag 'latest' to the concrete
		// number).
		probe, probeErr := exec.Command("claude", "--version").Output()
		if probeErr != nil {
			_, _ = io.WriteString(w, "err: post-install version probe failed: "+probeErr.Error()+"\n")
			flusher.Flush()
			return
		}
		v := strings.TrimSpace(string(probe))
		if i := strings.IndexByte(v, ' '); i > 0 {
			v = v[:i]
		}
		_, _ = io.WriteString(w, "ok: now at "+v+"\n")
		flusher.Flush()
	})
}
