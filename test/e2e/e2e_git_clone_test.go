//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestE2E_GitClone_PublicRepo verifies the container ships git and can
// clone a public HTTPS repository (no credentials needed). Uses
// octocat/Hello-World — GitHub's official 4-file demo repo, which has
// existed since 2011 and is tiny.
func TestE2E_GitClone_PublicRepo(t *testing.T) {
	tok, _ := login(t, testUser, testPassword)
	sid := createSession(t, tok, "git-clone")
	conn := dialWS(t, tok)

	// First, prove git is available.
	exit, out := runInSession(t, conn, sid, "git --version", 5*time.Second)
	if exit != 0 {
		t.Fatalf("git --version exit=%d out=%q", exit, out)
	}
	if !strings.Contains(out, "git version") {
		t.Fatalf("unexpected git --version output: %q", out)
	}

	// Use a per-test dir (timestamp via sid prefix) so re-runs don't collide.
	dir := "/tmp/clone-" + sid[:6]
	cmd := "rm -rf " + dir +
		" && git -c advice.detachedHead=false clone --depth 1 https://github.com/octocat/Hello-World.git " + dir +
		" && ls " + dir
	exit, out = runInSession(t, conn, sid, cmd, 30*time.Second)
	if exit != 0 {
		t.Fatalf("clone exit=%d out=%q", exit, out)
	}
	if !strings.Contains(out, "README") {
		t.Fatalf("expected README in cloned repo; got %q", out)
	}
}
