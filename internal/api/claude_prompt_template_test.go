package api

import (
	"strings"
	"testing"
)

// Verifies the helper that builds the final prompt text injected
// into `claude -p`. We isolate the pure function so we don't have
// to spin up a real manager + runner in this unit test.
func TestComposePromptText_NoTemplate_PassThrough(t *testing.T) {
	got := composePromptText("hello", "" /*id*/, "01SID", "/data/summaries/01SID.md")
	if got != "hello" {
		t.Errorf("composePromptText=%q, want %q", got, "hello")
	}
}

func TestComposePromptText_WithTemplate_Appended(t *testing.T) {
	got := composePromptText("hello", "summary-todo", "01SID", "/data/summaries/01SID.md")
	if !strings.HasPrefix(got, "hello\n\n---\n") {
		t.Errorf("template not appended after delimiter; got prefix %q", got[:minInt(60, len(got))])
	}
	if !strings.Contains(got, "/data/summaries/01SID.md") {
		t.Error("rendered template missing summary path substitution")
	}
	if !strings.Contains(got, "01SID") {
		t.Error("rendered template missing sid substitution")
	}
}

func TestComposePromptText_UnknownTemplate_PassThrough(t *testing.T) {
	got := composePromptText("hello", "no-such", "01SID", "/data/summaries/01SID.md")
	if got != "hello" {
		t.Errorf("composePromptText(unknown id)=%q, want passthrough", got)
	}
}

func minInt(a, b int) int { if a < b { return a }; return b }
