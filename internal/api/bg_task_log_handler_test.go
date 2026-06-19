package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudebgtasks"
)

// --- mock implementations ---

// mockMetaResolver satisfies MetaResolver without any real db.
type mockMetaResolver struct {
	uuid string
	err  error
}

func (m *mockMetaResolver) ClaudeUUIDFor(_ string) (string, error) {
	return m.uuid, m.err
}

// mockCWDResolver satisfies CWDResolver without touching tmux.
type mockCWDResolver struct {
	cwd string
	err error
}

func (m *mockCWDResolver) CWDFor(_ string) (string, error) {
	return m.cwd, m.err
}

// --- helper ---

// serveViaChiRouter mounts the handler on a chi router and fires the request.
// This ensures chi.URLParam works correctly.
func serveViaChiRouter(t *testing.T, h http.Handler, sid, taskID, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/bg-tasks/{taskId}/log", h.ServeHTTP)

	path := fmt.Sprintf("/api/sessions/%s/bg-tasks/%s/log", sid, taskID)
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- tests ---

// TestBgTaskLogHandler_NotFound: file doesn't exist at the computed path →
// HTTP 200, body has status:"log_unavailable".
func TestBgTaskLogHandler_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	meta := &mockMetaResolver{uuid: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	cwdr := &mockCWDResolver{cwd: dir}

	h := GetBgTaskLogHandler(meta, cwdr)
	w := serveViaChiRouter(t, h, "sess1", "mytask", "")

	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "log_unavailable" {
		t.Errorf("status = %q, want log_unavailable", body["status"])
	}
	if body["reason"] != "file_not_found" {
		t.Errorf("reason = %q, want file_not_found", body["reason"])
	}
}

// TestBgTaskLogHandler_SuccessfulTail: write a temp file with >tail bytes,
// request returns the last tail bytes correctly base64-encoded.
func TestBgTaskLogHandler_SuccessfulTail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	sessionUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	taskID := "task1"
	const tail = 32

	meta := &mockMetaResolver{uuid: sessionUUID}
	cwdr := &mockCWDResolver{cwd: dir}

	// Compute where OutputPath will write and create the file there.
	outPath := claudebgtasks.OutputPath(dir, sessionUUID, taskID)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// 64 bytes: first 32 'A', last 32 'B'.
	content := strings.Repeat("A", 32) + strings.Repeat("B", 32)
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	h := GetBgTaskLogHandler(meta, cwdr)
	w := serveViaChiRouter(t, h, "sess1", taskID, fmt.Sprintf("tail=%d", tail))

	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200; body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Bytes     string `json:"bytes"`
		Size      int    `json:"size"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Size != 64 {
		t.Errorf("size = %d, want 64", body.Size)
	}
	if !body.Truncated {
		t.Error("truncated = false, want true (size 64 > tail 32)")
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Bytes)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	want := content[len(content)-tail:] // last 32 bytes = all B's
	if string(decoded) != want {
		t.Errorf("decoded = %q, want %q", decoded, want)
	}
}

// TestBgTaskLogHandler_InvalidTaskID: .., very long (>20 chars), and
// invalid chars in taskID → 400.
//
// Note: taskIDs containing '/' (like "../etc") cannot be tested via chi
// routing because chi interprets '/' as a path separator. We test '..'
// directly (no slash) plus a 21-char ID and IDs with invalid chars. The
// regex ^[a-zA-Z0-9_-]{1,20}$ covers all these cases.
func TestBgTaskLogHandler_InvalidTaskID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	meta := &mockMetaResolver{uuid: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	cwdr := &mockCWDResolver{cwd: dir}
	h := GetBgTaskLogHandler(meta, cwdr)

	cases := []struct {
		taskID string
		desc   string
	}{
		{"..", "double-dot"},
		{strings.Repeat("x", 21), "21-chars (exceeds max 20)"},
		{"task.id", "dot (not in allowed set)"},
		{"task@id", "at-sign"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			w := serveViaChiRouter(t, h, "sess1", tc.taskID, "")
			if w.Code != http.StatusBadRequest {
				t.Errorf("taskID=%q: got %d want 400; body: %s", tc.taskID, w.Code, w.Body.String())
			}
		})
	}
}

// TestBgTaskLogHandler_TailDefault: no tail param → uses 8192.
// Write 10240 bytes, expect exactly 8192 bytes back, truncated=true.
func TestBgTaskLogHandler_TailDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	sessionUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	taskID := "defaulttail"

	meta := &mockMetaResolver{uuid: sessionUUID}
	cwdr := &mockCWDResolver{cwd: dir}

	outPath := claudebgtasks.OutputPath(dir, sessionUUID, taskID)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const fileSize = 10240
	content := strings.Repeat("C", fileSize)
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	h := GetBgTaskLogHandler(meta, cwdr)
	// No tail param — should default to 8192.
	w := serveViaChiRouter(t, h, "sess1", taskID, "")

	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200; body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Bytes     string `json:"bytes"`
		Size      int    `json:"size"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Bytes)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if len(decoded) != 8192 {
		t.Errorf("decoded len = %d, want 8192 (default tail)", len(decoded))
	}
	if body.Size != fileSize {
		t.Errorf("size = %d, want %d", body.Size, fileSize)
	}
	if !body.Truncated {
		t.Error("truncated = false, want true")
	}
}

// TestBgTaskLogHandler_TailClamped: ?tail=999999 → clamped to 65536.
// Write 70000 bytes, expect exactly 65536 bytes back.
func TestBgTaskLogHandler_TailClamped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALFRED_CLAUDE_BG_TASK_DIR", dir)

	sessionUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	taskID := "clamped"

	meta := &mockMetaResolver{uuid: sessionUUID}
	cwdr := &mockCWDResolver{cwd: dir}

	outPath := claudebgtasks.OutputPath(dir, sessionUUID, taskID)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const fileSize = 70000
	content := strings.Repeat("D", fileSize)
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	h := GetBgTaskLogHandler(meta, cwdr)
	w := serveViaChiRouter(t, h, "sess1", taskID, "tail=999999")

	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200; body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Bytes     string `json:"bytes"`
		Size      int    `json:"size"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Bytes)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if len(decoded) != 65536 {
		t.Errorf("decoded len = %d, want 65536 (max tail)", len(decoded))
	}
	if body.Size != fileSize {
		t.Errorf("size = %d, want %d", body.Size, fileSize)
	}
	if !body.Truncated {
		t.Error("truncated = false, want true")
	}
}
