package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSettingsHook_FreshInstall(t *testing.T) {
	home := t.TempDir()
	if err := EnsureSettingsHook(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("PreToolUse len = %d, want 1; raw=%s", len(pre), data)
	}
	first, _ := pre[0].(map[string]any)
	if first["matcher"] != ".*" {
		t.Errorf("matcher = %v, want .*", first["matcher"])
	}
	cmds, _ := first["hooks"].([]any)
	if len(cmds) != 1 {
		t.Fatalf("nested hooks len = %d, want 1", len(cmds))
	}
	cmd0, _ := cmds[0].(map[string]any)
	if cmd0["command"] != BridgeHookPath {
		t.Errorf("command = %v, want %q", cmd0["command"], BridgeHookPath)
	}
}

func TestEnsureSettingsHook_PreservesOtherKeys(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	_ = os.MkdirAll(dir, 0700)
	prior := []byte(`{"model":"opus","customKey":["a","b"]}`)
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), prior, 0600)

	if err := EnsureSettingsHook(home); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "opus" {
		t.Errorf("model = %v, lost", doc["model"])
	}
	if _, ok := doc["customKey"]; !ok {
		t.Errorf("customKey lost")
	}
	if _, ok := doc["hooks"]; !ok {
		t.Errorf("hooks block not added")
	}
}

func TestEnsureSettingsHook_Idempotent_DoesNotRewrite(t *testing.T) {
	home := t.TempDir()
	if err := EnsureSettingsHook(home); err != nil {
		t.Fatal(err)
	}
	st1, err := os.Stat(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	t1 := st1.ModTime()
	// Run again — should NOT rewrite, mtime stays.
	if err := EnsureSettingsHook(home); err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !st2.ModTime().Equal(t1) {
		t.Errorf("file was rewritten — mtime changed from %v to %v", t1, st2.ModTime())
	}
}

func TestEnsureSettingsHook_AddsHookToExistingHooksBlock(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	_ = os.MkdirAll(dir, 0700)
	// User has another hook category set up; we should add PreToolUse
	// without touching PostToolUse.
	prior := []byte(`{
  "hooks": {
    "PostToolUse": [{"matcher": ".*", "hooks": [{"type": "command", "command": "/usr/bin/true"}]}]
  }
}`)
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), prior, 0600)

	if err := EnsureSettingsHook(home); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Error("PostToolUse lost")
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("PreToolUse not added")
	}
}

func TestEnsureSettingsHook_RejectsBadJSON(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte("not json"), 0600)
	if err := EnsureSettingsHook(home); err == nil {
		t.Fatal("expected error on bad settings.json")
	}
}
