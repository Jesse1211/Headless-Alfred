package template

import (
	"strings"
	"testing"
)

func TestRender_SubstitutesPlaceholders(t *testing.T) {
	out := Render("summary-todo", RenderArgs{
		SessionID:   "01KSESSION",
		SummaryPath: "/data/summaries/01KSESSION.md",
	})
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
	if got := Render("does-not-exist", RenderArgs{SessionID: "X", SummaryPath: "/x"}); got != "" {
		t.Errorf("Render(unknown)=%q, want \"\"", got)
	}
}

func TestRender_EmptyIDReturnsEmpty(t *testing.T) {
	if got := Render("", RenderArgs{SessionID: "X", SummaryPath: "/x"}); got != "" {
		t.Errorf("Render(\"\")=%q, want \"\"", got)
	}
}

func TestRender_RecapDailySubstitutes(t *testing.T) {
	got := Render("recap-daily", RenderArgs{
		Date:      "2026-06-15",
		Cwd:       "/home/alfred",
		RecapPath: "/data/recaps/2026-06-15.md",
	})
	if !strings.Contains(got, "2026-06-15") {
		t.Errorf("date not substituted: %q", got)
	}
	if !strings.Contains(got, "/home/alfred") {
		t.Errorf("cwd not substituted: %q", got)
	}
	if !strings.Contains(got, "/data/recaps/2026-06-15.md") {
		t.Errorf("recap_path not substituted: %q", got)
	}
	if strings.Contains(got, "<date>") || strings.Contains(got, "<cwd>") || strings.Contains(got, "<recap_path>") {
		t.Errorf("Render left placeholder unsubstituted: %s", got)
	}
	if !strings.Contains(got, "git log") {
		t.Errorf("git lookup missing")
	}
	if !strings.Contains(got, "claude-mem") {
		t.Errorf("claude-mem mention missing")
	}
}
