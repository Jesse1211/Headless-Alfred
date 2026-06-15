package template

import (
	"strings"
	"testing"
)

func TestRender_SubstitutesPlaceholders(t *testing.T) {
	out := Render("summary-todo", "01KSESSION", "/data/summaries/01KSESSION.md")
	if out == "" {
		t.Fatal("Render returned empty for known id")
	}
	if !strings.Contains(out, "01KSESSION") {
		t.Error("Render did not substitute <sid>")
	}
	if !strings.Contains(out, "/data/summaries/01KSESSION.md") {
		t.Error("Render did not substitute <summary_path>")
	}
	if strings.Contains(out, "<sid>") || strings.Contains(out, "<summary_path>") {
		t.Errorf("Render left placeholder unsubstituted: %s", out)
	}
}

func TestRender_UnknownIDReturnsEmpty(t *testing.T) {
	if got := Render("does-not-exist", "X", "/x"); got != "" {
		t.Errorf("Render(unknown)=%q, want \"\"", got)
	}
}

func TestRender_EmptyIDReturnsEmpty(t *testing.T) {
	if got := Render("", "X", "/x"); got != "" {
		t.Errorf("Render(\"\")=%q, want \"\"", got)
	}
}
