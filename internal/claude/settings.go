package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ProdBridgeHookPath is where the production image (Dockerfile) puts
// the hook script. Always available in the pod. Local dev binaries
// don't have it — resolveBridgePath falls back to installing a copy
// under $HOME/.local on dev machines.
const ProdBridgeHookPath = "/usr/local/bin/alfred-claude-bridge"

// bridgeScriptBody is the curl-wrapper that the hook command invokes.
// Kept in sync with deploy/alfred-claude-bridge.sh — that's the
// canonical source for prod, this string is the dev fallback.
const bridgeScriptBody = `#!/bin/sh
set -e
exec curl --silent --show-error \
     --max-time 600 \
     --header 'Content-Type: application/json' \
     --data-binary @- \
     http://127.0.0.1:8090/tool-approval
`

// resolveBridgePath returns the path to the bridge script that the
// PreToolUse hook should invoke. If the prod path exists and is
// executable, use it. Otherwise install a copy under $HOME/.local
// and use that. Returns an error only if neither path is usable.
func resolveBridgePath(home string) (string, error) {
	if st, err := os.Stat(ProdBridgeHookPath); err == nil && !st.IsDir() {
		return ProdBridgeHookPath, nil
	}
	if home == "" {
		return "", fmt.Errorf("home dir required to install bridge fallback")
	}
	dir := filepath.Join(home, ".local", "share", "alfred")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "alfred-claude-bridge")
	// Always rewrite — body is tiny, ensures we stay in sync with
	// any change to bridgeScriptBody after upgrades.
	if err := os.WriteFile(path, []byte(bridgeScriptBody), 0755); err != nil {
		return "", fmt.Errorf("install bridge script: %w", err)
	}
	return path, nil
}

// EnsureSettingsHook patches ~/.claude/settings.json so claude's
// PreToolUse hook fires our bridge script. Idempotent: if the
// settings.json already has a PreToolUse entry pointing at the
// bridge, this is a no-op. If settings.json exists with other keys,
// we leave them alone — we only modify the hooks.PreToolUse path.
//
// Atomic write via tmp + rename.
func EnsureSettingsHook(home string) error {
	if home == "" {
		return fmt.Errorf("home dir required")
	}
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "settings.json")
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read settings.json: %w", err)
	}

	// Decode (or start fresh).
	var settings map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &settings); err != nil {
			return fmt.Errorf("parse existing settings.json: %w", err)
		}
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}

	// Pull existing hooks block (if any).
	var hooks map[string]json.RawMessage
	if raw, ok := settings["hooks"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return fmt.Errorf("parse hooks block: %w", err)
		}
	}
	if hooks == nil {
		hooks = map[string]json.RawMessage{}
	}

	bridgePath, err := resolveBridgePath(home)
	if err != nil {
		return fmt.Errorf("resolve bridge path: %w", err)
	}

	// Desired PreToolUse value: one entry matching ".*", running our
	// bridge script as a command hook.
	desired := []hookMatcher{{
		Matcher: ".*",
		Hooks: []hookCommand{{
			Type:    "command",
			Command: bridgePath,
		}},
	}}
	desiredJSON, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("marshal desired hooks: %w", err)
	}

	// If the existing PreToolUse already points at this bridge path,
	// leave the user's wider config alone.
	if cur, ok := hooks["PreToolUse"]; ok && containsBridge(cur, bridgePath) {
		return nil
	}
	hooks["PreToolUse"] = desiredJSON

	hooksJSON, err := json.Marshal(hooks)
	if err != nil {
		return fmt.Errorf("marshal hooks block: %w", err)
	}
	settings["hooks"] = hooksJSON

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "settings.json.tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

type hookMatcher struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookCommand `json:"hooks"`
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func containsBridge(raw json.RawMessage, wantPath string) bool {
	var matchers []hookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return false
	}
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if h.Command == wantPath {
				return true
			}
		}
	}
	return false
}
