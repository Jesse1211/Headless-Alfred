package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnthropicCredentialsHandler_WritesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	body := `{"claudeAiOauth":{"accessToken":"sk-ant-oat-fake","refreshToken":"r","expiresAt":9999999999000,"scopes":["user:inference"]}}`
	req := httptest.NewRequest("POST", "/api/anthropic-credentials", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	AnthropicCredentialsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}

	credPath := filepath.Join(tmp, ".claude", ".credentials.json")
	got, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != body {
		t.Fatalf("written body does not match input")
	}

	st, err := os.Stat(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Errorf("file mode = %o, want 0600", st.Mode().Perm())
	}

	dir, _ := os.Stat(filepath.Join(tmp, ".claude"))
	if dir.Mode().Perm() != 0700 {
		t.Errorf("dir mode = %o, want 0700", dir.Mode().Perm())
	}
}

func TestAnthropicCredentialsHandler_AtomicRename(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	credDir := filepath.Join(tmp, ".claude")
	credPath := filepath.Join(credDir, ".credentials.json")

	// Seed an existing valid file the user might already have.
	_ = os.MkdirAll(credDir, 0700)
	_ = os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"accessToken":"OLD"}}`), 0600)

	body := `{"claudeAiOauth":{"accessToken":"NEW","refreshToken":"r","expiresAt":9999999999000,"scopes":["x"]}}`
	req := httptest.NewRequest("POST", "/api/anthropic-credentials", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	AnthropicCredentialsHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}

	got, _ := os.ReadFile(credPath)
	if !strings.Contains(string(got), `"accessToken":"NEW"`) {
		t.Fatalf("file not overwritten with new content: %s", got)
	}
	// No leftover tmp files in the dir.
	entries, _ := os.ReadDir(credDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".credentials.json.tmp.") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestAnthropicCredentialsHandler_Validation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cases := []struct {
		name string
		body string
		code int
	}{
		{"empty body", "", http.StatusBadRequest},
		{"bad json", "{not json", http.StatusBadRequest},
		{"missing oauth section", `{"foo":"bar"}`, http.StatusUnprocessableEntity},
		{"empty access token", `{"claudeAiOauth":{"accessToken":""}}`, http.StatusUnprocessableEntity},
		{"missing access token field", `{"claudeAiOauth":{"refreshToken":"r"}}`, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/anthropic-credentials", bytes.NewReader([]byte(c.body)))
			rec := httptest.NewRecorder()
			AnthropicCredentialsHandler().ServeHTTP(rec, req)
			if rec.Code != c.code {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, c.code, rec.Body.String())
			}
		})
	}
}

func TestAnthropicCredentialsHandler_TooLarge(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// 65 KiB body — over the 64 KiB cap.
	big := bytes.Repeat([]byte("a"), 65*1024)
	req := httptest.NewRequest("POST", "/api/anthropic-credentials", bytes.NewReader(big))
	rec := httptest.NewRecorder()
	AnthropicCredentialsHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}
