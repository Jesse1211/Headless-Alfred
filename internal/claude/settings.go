package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BridgeHookPath is the in-image path of the PreToolUse hook script
// shipped by the runtime Dockerfile.
const BridgeHookPath = "/usr/local/bin/alfred-claude-bridge"

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

	// Desired PreToolUse value: one entry matching ".*", running our
	// bridge script as a command hook.
	desired := []hookMatcher{{
		Matcher: ".*",
		Hooks: []hookCommand{{
			Type:    "command",
			Command: BridgeHookPath,
		}},
	}}
	desiredJSON, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("marshal desired hooks: %w", err)
	}

	// If the existing PreToolUse already includes our bridge, leave
	// the user's wider config alone. Otherwise we overwrite the
	// PreToolUse key — that's the only key this function owns.
	if cur, ok := hooks["PreToolUse"]; ok && containsBridge(cur) {
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

func containsBridge(raw json.RawMessage) bool {
	var matchers []hookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return false
	}
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if h.Command == BridgeHookPath {
				return true
			}
		}
	}
	return false
}
