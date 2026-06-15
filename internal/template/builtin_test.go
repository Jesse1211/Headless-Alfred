package template

import "testing"

func TestBuiltins_SummaryTodoIsRegistered(t *testing.T) {
	t.Helper()
	tpl, ok := Builtins["summary-todo"]
	if !ok {
		t.Fatal("Builtins[summary-todo] missing")
	}
	if tpl.ID != "summary-todo" {
		t.Errorf("ID=%q want summary-todo", tpl.ID)
	}
	if tpl.Name == "" {
		t.Error("Name must be non-empty")
	}
	if len(tpl.Content) < 100 {
		t.Errorf("Content too short (%d bytes); expected the full task-list instructions", len(tpl.Content))
	}
	// The two placeholders we'll substitute later must appear verbatim.
	for _, marker := range []string{"<sid>", "<summary_path>"} {
		if !contains(tpl.Content, marker) {
			t.Errorf("Content missing placeholder %q", marker)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
