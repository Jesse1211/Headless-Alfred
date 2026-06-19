package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jesseliu/headless-alfred/internal/claudestate"
)

// stub locator that always says "not found"
type stubLocator struct{}

func (stubLocator) Locate(sessionID, claudeUUID string) (string, error) { return "", os.ErrNotExist }

func TestClaudeStateHandler_HappyPath(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	// Seed one turn so the response is non-trivial.
	st, err := mgr.GetOrLoad("sess1", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	st.BeginTurn("u1", "hi", time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC))

	r := httptest.NewRequest(http.MethodGet,
		"/api/sessions/sess1/claude-state", nil)
	r = withChiParam(r, "sid", "sess1")
	w := httptest.NewRecorder()

	GetClaudeStateHandler(mgr, &stubMetaResolver{uuid: "uuid-1"}).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var got claudestate.ClaudeState
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if len(got.Turns) != 1 || got.Turns[0].ID != "u1" {
		t.Errorf("turns: %+v", got.Turns)
	}
	if !got.TurnsLoaded {
		t.Error("TurnsLoaded should be true")
	}
}

func TestClaudeStateHandler_UnknownSessionReturns404(t *testing.T) {
	dir := t.TempDir()
	mgr := claudestate.NewSessionManager(dir, stubLocator{})
	defer mgr.Shutdown(context.Background())

	r := httptest.NewRequest(http.MethodGet, "/api/sessions/ghost/claude-state", nil)
	r = withChiParam(r, "sid", "ghost")
	w := httptest.NewRecorder()

	GetClaudeStateHandler(mgr, &stubMetaResolver{notFound: true}).ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d, want 404", w.Code)
	}
}

// withChiParam injects {sid} into the chi route context so handler
// code that calls chi.URLParam(r, "sid") works under httptest.
func withChiParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

type stubMetaResolver struct {
	uuid     string
	notFound bool
}

func (s *stubMetaResolver) ClaudeUUIDFor(sessionID string) (string, error) {
	if s.notFound {
		return "", ErrUnknownSession
	}
	return s.uuid, nil
}

// suppress unused warnings for imports in the test file
var _ = errors.New
