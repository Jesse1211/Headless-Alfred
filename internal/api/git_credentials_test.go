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

func TestGitCredentialsHandler_WritesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	body := `{"host":"github.com","username":"alice","token":"ghp_secret123"}`
	req := httptest.NewRequest("POST", "/api/git-credentials", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	GitCredentialsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}

	credBytes, err := os.ReadFile(filepath.Join(tmp, ".git-credentials"))
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	got := string(credBytes)
	want := "https://alice:ghp_secret123@github.com\n"
	if got != want {
		t.Fatalf("credentials = %q, want %q", got, want)
	}

	cfgBytes, err := os.ReadFile(filepath.Join(tmp, ".gitconfig"))
	if err != nil {
		t.Fatalf("read gitconfig: %v", err)
	}
	if !strings.Contains(string(cfgBytes), "helper = store") {
		t.Fatalf("gitconfig missing helper=store: %s", cfgBytes)
	}
}

func TestGitCredentialsHandler_ReplacesSameHost(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Pre-seed with an old credential for github.com plus another for gitlab.
	seed := "https://old:oldtok@github.com\nhttps://bob:gltok@gitlab.com\n"
	_ = os.WriteFile(filepath.Join(tmp, ".git-credentials"), []byte(seed), 0600)

	body := `{"host":"github.com","username":"alice","token":"newtok"}`
	req := httptest.NewRequest("POST", "/api/git-credentials", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	GitCredentialsHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}

	got, _ := os.ReadFile(filepath.Join(tmp, ".git-credentials"))
	s := string(got)
	if strings.Contains(s, "old:oldtok") {
		t.Fatalf("did not replace old github credential: %s", s)
	}
	if !strings.Contains(s, "alice:newtok@github.com") {
		t.Fatalf("missing new github credential: %s", s)
	}
	if !strings.Contains(s, "bob:gltok@gitlab.com") {
		t.Fatalf("clobbered unrelated gitlab credential: %s", s)
	}
}

func TestGitCredentialsHandler_GitconfigIdempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Pre-existing gitconfig already containing the helper line.
	seed := "[user]\n\temail = me@example.com\n[credential]\n\thelper = store\n"
	_ = os.WriteFile(filepath.Join(tmp, ".gitconfig"), []byte(seed), 0600)

	body := `{"host":"github.com","username":"alice","token":"tok"}`
	req := httptest.NewRequest("POST", "/api/git-credentials", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	GitCredentialsHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, ".gitconfig"))
	if strings.Count(string(got), "helper = store") != 1 {
		t.Fatalf("helper appended twice: %s", got)
	}
	if !strings.Contains(string(got), "email = me@example.com") {
		t.Fatalf("clobbered existing user section: %s", got)
	}
}

func TestGitCredentialsHandler_Validation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cases := []struct {
		name string
		body string
		code int
	}{
		{"missing host", `{"username":"a","token":"t"}`, http.StatusUnprocessableEntity},
		{"host with scheme", `{"host":"https://github.com","username":"a","token":"t"}`, http.StatusUnprocessableEntity},
		{"host with slash", `{"host":"github.com/x","username":"a","token":"t"}`, http.StatusUnprocessableEntity},
		{"missing username", `{"host":"github.com","token":"t"}`, http.StatusUnprocessableEntity},
		{"missing token", `{"host":"github.com","username":"a"}`, http.StatusUnprocessableEntity},
		{"bad json", `{not-json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/git-credentials", bytes.NewReader([]byte(c.body)))
			rec := httptest.NewRecorder()
			GitCredentialsHandler().ServeHTTP(rec, req)
			if rec.Code != c.code {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, c.code, rec.Body.String())
			}
		})
	}
}
