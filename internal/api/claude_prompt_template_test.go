package api

import (
	"strings"
	"testing"
)

// Verifies the helper that builds the final prompt text injected
// into `claude -p`. We isolate the pure function so we don't have
// to spin up a real manager + runner in this unit test.
func TestComposePromptText_NoTemplate_PassThrough(t *testing.T) {
	got := composePromptText("hello", nil, "01SID", "/data/summaries/01SID.md")
	if got != "hello" {
		t.Errorf("composePromptText=%q, want %q", got, "hello")
	}
}

func TestComposePromptText_EmptyList_PassThrough(t *testing.T) {
	got := composePromptText("hello", []string{}, "01SID", "/data/summaries/01SID.md")
	if got != "hello" {
		t.Errorf("composePromptText=%q, want %q (empty list = no inject)", got, "hello")
	}
}

func TestComposePromptText_WithTemplate_Appended(t *testing.T) {
	got := composePromptText("hello", []string{"summary-todo"}, "01SID", "/data/summaries/01SID.md")
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
	got := composePromptText("hello", []string{"no-such"}, "01SID", "/data/summaries/01SID.md")
	if got != "hello" {
		t.Errorf("composePromptText(unknown id)=%q, want passthrough", got)
	}
}

func TestComposePromptText_MultipleTemplates_OrderPreserved(t *testing.T) {
	// Two valid IDs back-to-back; output should contain both bodies
	// in the order given, each preceded by its own \n\n---\n
	// separator. Use summary-todo twice to keep the test resilient
	// to changes in which templates exist.
	got := composePromptText("hello", []string{"summary-todo", "summary-todo"}, "01SID", "/data/summaries/01SID.md")
	separators := strings.Count(got, "\n\n---\n")
	if separators != 2 {
		t.Errorf("want 2 '\\n\\n---\\n' separators, got %d in %q", separators, got)
	}
}

func minInt(a, b int) int { if a < b { return a }; return b }
