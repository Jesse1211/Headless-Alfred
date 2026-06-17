package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudehistory"
)

// sessionIDLookup is a tiny stub matching the subset of *session.Manager
// the handler actually uses. Lets us test without spinning up tmux.
type sessionIDLookup interface {
	GetClaudeSessionID(sessionID string) string
}

type fakeLookup struct {
	m map[string]string
}

func (f *fakeLookup) GetClaudeSessionID(sid string) string { return f.m[sid] }

func newTestHandler(t *testing.T, lookup sessionIDLookup) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/sessions/{sid}/claude-history",
		GetClaudeHistoryHandler(lookup, claudehistory.NewLocator()).ServeHTTP)
	return r
}

func writeFixtureJsonl(t *testing.T, uuid, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, uuid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeHistory_NoUUIDReturnsEmpty(t *testing.T) {
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": ""}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q (want [])", w.Body.String())
	}
}

func TestClaudeHistory_MissingFileReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": "ghost-uuid"}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q (want [])", w.Body.String())
	}
}

func TestClaudeHistory_ReturnsParsedTurns(t *testing.T) {
	uuid := "uuid-test"
	writeFixtureJsonl(t, uuid, strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-06-15T10:00:00.000Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
	}, "\n"))
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": uuid}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	var got []claudehistory.Turn
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(got) != 1 || got[0].Prompt != "hi" {
		t.Errorf("got %+v", got)
	}
	// Reply text is now an array of blocks; pluck the first text block.
	if len(got[0].Blocks) == 0 || got[0].Blocks[0].Kind != "text" || got[0].Blocks[0].Text != "hello" {
		t.Errorf("blocks = %+v, want one text block 'hello'", got[0].Blocks)
	}
}

func TestClaudeHistory_LimitClampedTo500(t *testing.T) {
	uuid := "uuid-clamp"
	// Build 3 turns; ask for limit=999 → response is still all 3, no error.
	lines := []string{}
	for i := 1; i <= 3; i++ {
		lines = append(lines,
			`{"type":"user","message":{"role":"user","content":"q`+itoa(i)+`"},"uuid":"u`+itoa(i)+`","timestamp":"2026-06-15T10:00:00.000Z"}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a`+itoa(i)+`"}]}}`)
	}
	writeFixtureJsonl(t, uuid, strings.Join(lines, "\n"))
	h := newTestHandler(t, &fakeLookup{m: map[string]string{"sid-A": uuid}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/sid-A/claude-history?limit=999", nil)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var got []claudehistory.Turn
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 3 {
		t.Errorf("got %d turns, want 3", len(got))
	}
}

// itoa avoids importing strconv just for tests.
func itoa(i int) string { return string(rune('0' + i)) }
